package server

import (
	"context"
	"sort"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// Per-project usage: the window's spend and the week's, by project and vendor.
//
// The window figures come free — the per-vendor Report already scanned the
// window and kept its per-directory breakdown. The week does not: a week of
// transcripts is several times a day's, so it is scanned on its own slower
// cadence and held between scans, and its start is the one thing here the logs
// cannot say — it is the vendor's quota reset, which comes from a limits source.

const (
	// week is the length of every vendor quota week baton knows.
	week = 7 * 24 * time.Hour

	// defaultWeekEvery is how often the week is rescanned when nothing forces it
	// sooner. Five minutes against a thirty-second usage tick: measured, a week is
	// ~3x a day's transcripts, and rescanning it every tick would triple the
	// daemon's steady disk read for figures that move by one turn at a time.
	defaultWeekEvery = 5 * time.Minute

	// maxProjectRows is how many projects travel on the wire before the rest are
	// summed into ProjectOther. It caps a payload broadcast to every client every
	// time a figure moves; a table past twenty rows is not read anyway.
	maxProjectRows = 20
)

// weekUsage is the week scan's state. It is touched only by the usage loop's
// goroutine (refreshUsage), so it has no lock of its own and is not under mu —
// the scan it drives is exactly the disk work mu must not be held across.
type weekUsage struct {
	every time.Duration    // rescan cadence; zero means defaultWeekEvery
	now   func() time.Time // injectable clock; nil means time.Now

	// resets is the last quota reset seen per vendor, stepped forward a week at a
	// time once it passes. It is REMEMBERED rather than read afresh because a
	// limits read fails, or has nothing, routinely — and a week that turned into a
	// rolling seven days whenever that happened would change meaning under the
	// operator between two polls.
	resets map[string]time.Time

	held  map[string]weekFigure // the last successful week scan per vendor
	ranAt time.Time             // when the week was last scanned; zero before the first
}

// weekFigure is one vendor's week scan and what its start was.
type weekFigure struct {
	since time.Time
	quota bool // since is the vendor's quota week start, not merely now - 7d
	snap  usage.Snapshot
}

// WithWeekUsage sets how often the per-project week figures are rescanned. Zero
// keeps the default. It only matters with the per-vendor list on
// (WithVendorUsage); a test passes a long cadence to hold a scan still.
func WithWeekUsage(every time.Duration) Option {
	return func(s *Server) { s.week.every = every }
}

func (w *weekUsage) clock() time.Time {
	if w.now == nil {
		return time.Now()
	}
	return w.now()
}

func (w *weekUsage) interval() time.Duration {
	if w.every <= 0 {
		return defaultWeekEvery
	}
	return w.every
}

// start is where a vendor's week begins now, given the reset read this tick
// (zero when there was none). A fresh reading replaces the remembered one; a
// remembered one steps forward a week at a time until it is ahead of now, since
// a quota week that has ended is followed by the next, reading or not. Only a
// vendor that has never stated a reset falls back to a rolling seven days.
//
// passed reports that the remembered reset has gone by since the last call: the
// week the held figures describe is over, and they must be rescanned now rather
// than on the cadence.
func (w *weekUsage) start(vendor string, seen, now time.Time) (since time.Time, quota, passed bool) {
	r := w.resets[vendor]
	passed = !r.IsZero() && !r.After(now)
	if !seen.IsZero() {
		r = seen
	}
	if r.IsZero() {
		return now.Add(-week), false, false
	}
	for !r.After(now) {
		r = r.Add(week)
	}
	if w.resets == nil {
		w.resets = make(map[string]time.Time)
	}
	w.resets[vendor] = r
	return r.Add(-week), true, passed
}

// weekResetSeen is the quota reset a vendor stated this tick, or zero. grok's
// arrives on its report as the 7d window WithGrokLimits attached; Claude's is
// the account's seven-day limit, which the limits source reads for the whole
// account rather than for a vendor row.
func weekResetSeen(r usage.VendorReport, lim *proto.LimitsInfo) time.Time {
	for _, w := range r.Windows {
		if w.Label == usage.WindowWeek {
			return w.ResetsAt
		}
	}
	if r.Vendor == "claude" && lim != nil && lim.SevenDay != nil {
		if t, err := time.Parse(time.RFC3339, lim.SevenDay.ResetsAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

// weekEligible reports whether a vendor's week can be scanned at all: installed,
// and a reader exists. A vendor whose window scan failed this tick still counts —
// its week is a separate read that may well work.
func weekEligible(r usage.VendorReport) bool {
	return r.State != usage.VendorAbsent && usage.HasVendorReader(r.Vendor)
}

// refreshWeek rescans the week when one is due and returns the held figures for
// every eligible vendor. It runs outside mu: it walks a week of session logs.
//
// A scan is due on the first poll, once the cadence has elapsed, when a
// remembered reset has passed, and when a vendor states a reset for the first
// time (its held figure is a rolling week and now has a real start). Between
// scans the held figure stands, start included — the WeekSince on the wire
// always describes the figures beside it.
//
// A vendor whose scan fails keeps what it had: a week read short by an unknown
// amount is worse than one read five minutes ago. It is not retried before the
// cadence comes round, either — a scan that failed by running out of time would
// fail the same way on every thirty-second tick.
func (s *Server) refreshWeek(reports []usage.VendorReport, lim *proto.LimitsInfo) map[string]weekFigure {
	w := &s.week
	now := w.clock()
	due := w.ranAt.IsZero() || now.Sub(w.ranAt) >= w.interval()
	type plan struct {
		since time.Time
		quota bool
	}
	plans := make(map[string]plan)
	for _, r := range reports {
		if !weekEligible(r) {
			continue
		}
		since, quota, passed := w.start(r.Vendor, weekResetSeen(r, lim), now)
		plans[r.Vendor] = plan{since, quota}
		if held, ok := w.held[r.Vendor]; passed || (ok && quota && !held.quota) {
			due = true
		}
	}
	if due {
		ctx, cancel := context.WithTimeout(context.Background(), s.weekScanTimeout())
		defer cancel()
		for vendor, p := range plans {
			snap, ok, err := usage.VendorSince(ctx, vendor, p.since)
			if err != nil {
				log.Warn().Err(err).Str("vendor", vendor).Msg("week usage scan failed; keeping the last figures")
				continue
			}
			if !ok {
				continue
			}
			if w.held == nil {
				w.held = make(map[string]weekFigure)
			}
			w.held[vendor] = weekFigure{since: p.since, quota: p.quota, snap: snap}
		}
		w.ranAt = now
	}
	out := make(map[string]weekFigure, len(plans))
	for vendor := range plans {
		if f, ok := w.held[vendor]; ok {
			out[vendor] = f
		}
	}
	return out
}

// weekScanTimeout bounds one week scan: the cadence, but never more than two
// usage ticks. The scan runs on the usage loop's goroutine — the one that also
// refreshes the rate-limit bars and the window figures — so a week scan allowed
// the whole five-minute cadence could freeze the quota bars for five minutes on a
// slow disk. Two ticks lets a big week finish while costing the bars at most one
// missed refresh; a scan that needs longer fails, keeps its last figures, and is
// tried again at the next cadence. A server with no tick (a test) keeps the
// cadence as its bound.
func (s *Server) weekScanTimeout() time.Duration {
	t := s.week.interval()
	if s.usageInterval > 0 {
		t = min(t, 2*s.usageInterval)
	}
	return t
}

// projectScope is one vendor's per-directory spend over one scope.
type projectScope struct {
	vendor   string
	week     bool // the week scope; false is the window
	projects map[string]usage.SessionUsage
	hints    map[string]usage.ProjectHint
}

// projectScopes gathers the window scope of every reading vendor and the week
// scope of every vendor with a held week figure.
func projectScopes(reports []usage.VendorReport, weeks map[string]weekFigure) []projectScope {
	var out []projectScope
	for _, r := range reports {
		if r.State == usage.VendorReading && len(r.Projects) > 0 {
			out = append(out, projectScope{vendor: r.Vendor, projects: r.Projects, hints: r.ProjectHints})
		}
		if f, ok := weeks[r.Vendor]; ok && len(f.snap.Projects) > 0 {
			out = append(out, projectScope{vendor: r.Vendor, week: true, projects: f.snap.Projects, hints: f.snap.ProjectHints})
		}
	}
	return out
}

// projectRows turns every scope's per-directory spend into the wire rows.
//
// Labels are made ONCE, over every directory of every vendor in both scopes, so
// a directory is named the same in the window and the week, and a grok worktree
// can fold onto a repository only Claude has worked in. A directory with no
// hint still goes in, so it is named (unresolved) rather than dropped.
//
// Everything that sums walks in a fixed order — directories sorted, rows in wire
// order — because a float sum in map order differs in its last bits from poll to
// poll, and a cost that "changed" by 1e-17 would broadcast to every client.
func projectRows(scopes []projectScope, keep int) []proto.ProjectUsage {
	if len(scopes) == 0 {
		return nil
	}
	hints := make(map[usage.ProjectKey][]usage.ProjectHint)
	for _, sc := range scopes {
		for dir := range sc.projects {
			k := usage.ProjectKey{Vendor: sc.vendor, Dir: dir}
			h, ok := sc.hints[dir]
			if ok {
				hints[k] = append(hints[k], h)
			} else if _, seen := hints[k]; !seen {
				hints[k] = nil
			}
		}
	}
	labels := usage.LabelProjects(hints)

	type rowKey struct{ label, vendor string }
	rows := make(map[rowKey]*proto.ProjectUsage)
	weekOf := make(map[string]int64) // a label's week tokens across vendors, its rank
	for _, sc := range scopes {
		dirs := make([]string, 0, len(sc.projects))
		for dir := range sc.projects {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			u := sc.projects[dir]
			l := labels[usage.ProjectKey{Vendor: sc.vendor, Dir: dir}]
			k := rowKey{l, sc.vendor}
			r := rows[k]
			if r == nil {
				r = &proto.ProjectUsage{Project: l, Vendor: sc.vendor}
				rows[k] = r
			}
			if sc.week {
				r.WeekTokens += u.Tokens
				r.WeekCostUSD += u.CostUSD
			} else {
				r.SessionTokens += u.Tokens
				r.SessionCostUSD += u.CostUSD
			}
			w := weekOf[l] // every label is ranked, a window-only one at zero
			if sc.week {
				w += u.Tokens
			}
			weekOf[l] = w
		}
	}

	order := make([]string, 0, len(weekOf))
	for l := range weekOf {
		order = append(order, l)
	}
	sort.Slice(order, func(i, j int) bool {
		if weekOf[order[i]] != weekOf[order[j]] {
			return weekOf[order[i]] > weekOf[order[j]]
		}
		return order[i] < order[j]
	})
	byLabel := make(map[string][]*proto.ProjectUsage)
	for k, r := range rows {
		byLabel[k.label] = append(byLabel[k.label], r)
	}

	out := make([]proto.ProjectUsage, 0, min(len(rows), keep*2))
	other := make(map[string]*proto.ProjectUsage)
	for i, l := range order {
		vs := byLabel[l]
		sort.Slice(vs, func(a, b int) bool { return vs[a].Vendor < vs[b].Vendor })
		for _, r := range vs {
			if i < keep {
				out = append(out, *r)
				continue
			}
			o := other[r.Vendor]
			if o == nil {
				o = &proto.ProjectUsage{Project: proto.ProjectOther, Vendor: r.Vendor}
				other[r.Vendor] = o
			}
			o.SessionTokens += r.SessionTokens
			o.SessionCostUSD += r.SessionCostUSD
			o.WeekTokens += r.WeekTokens
			o.WeekCostUSD += r.WeekCostUSD
		}
	}
	vendors := make([]string, 0, len(other))
	for v := range other {
		vendors = append(vendors, v)
	}
	sort.Strings(vendors)
	for _, v := range vendors {
		out = append(out, *other[v])
	}
	return out
}

// attachProjects returns info carrying the project rows, without mutating what
// the caller held (see attachVendors). Nil rows leave info alone.
func attachProjects(info *proto.UsageInfo, rows []proto.ProjectUsage) *proto.UsageInfo {
	if rows == nil {
		return info
	}
	if info == nil {
		return &proto.UsageInfo{Projects: rows}
	}
	out := *info
	out.Projects = rows
	return &out
}
