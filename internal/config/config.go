// Package config is baton's persistent client configuration: a small YAML file
// at $HOME/.baton/config. Today it stores the user's key-binding overrides; it
// is the place future per-user settings land.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/restart"
	"github.com/cmj0121/baton/internal/usage"
)

// Config is the on-disk client configuration.
type Config struct {
	// Prefix is the leader key pressed before a binding, e.g. "ctrl+t".
	Prefix string `yaml:"prefix,omitempty"`

	// Keys maps a binding's stable name to the key pressed after the prefix,
	// e.g. {"new-panel": "p", "close": "w"}.
	Keys map[string]string `yaml:"keys,omitempty"`

	// Settings holds the cockpit toggles.
	Settings Settings `yaml:"settings,omitempty"`

	// Panel holds the default behaviour for new panels.
	Panel PanelDefaults `yaml:"panel,omitempty"`

	// Queue holds the task-backlog caps.
	Queue QueueDefaults `yaml:"queue,omitempty"`

	// Usage configures the account usage/cost footer segment.
	Usage UsageConfig `yaml:"usage,omitempty"`

	// TUI holds the cockpit appearance — the colour theme and the group-split
	// layouts. Its canonical source is a separate file ($HOME/.baton/TUI.yaml,
	// see LoadTUI); it lives here so it rides the same config broadcast to every
	// frontend.
	TUI TUIConfig `yaml:"tui,omitempty"`
}

// TUIConfig is the cockpit appearance: the colour theme and the named group-split
// layouts. It is read from $HOME/.baton/TUI.yaml (see LoadTUI) and carried on
// Config.TUI so frontends receive it over the existing config broadcast.
type TUIConfig struct {
	// Theme overrides the cockpit palette; an empty field keeps the built-in.
	Theme Theme `yaml:"theme,omitempty"`

	// Layouts are the named group-split arrangements offered in addition to the
	// built-in presets (tiled, main-vertical, main-horizontal, stack). A custom
	// entry with the same name as a preset overrides it.
	Layouts []Layout `yaml:"layouts,omitempty"`

	// DefaultLayout names the layout a group opens with; empty uses "tiled".
	DefaultLayout string `yaml:"default-layout,omitempty"`

	// Scratch configures the floating scratch pane (the pop-up shell overlaid on any
	// view): the command it runs and its size. An empty section uses the defaults.
	Scratch ScratchConfig `yaml:"scratch,omitempty"`
}

// ScratchConfig is the floating scratch pane's setup: the program it runs (empty =
// the panel shell) and its size as a fraction of the terminal (0 = the built-in
// default). It is read from TUI.yaml alongside the theme and layouts, so a SIGHUP
// hot-reloads it like any other appearance setting.
type ScratchConfig struct {
	Command string  `yaml:"command,omitempty"` // program to run; empty = the default shell
	Width   float64 `yaml:"width,omitempty"`   // width as a fraction of the terminal (0 = default)
	Height  float64 `yaml:"height,omitempty"`  // height as a fraction of the terminal (0 = default)
}

// Theme is the cockpit colour palette. Each field is a colour string (a hex
// "#rrggbb" or an ANSI index); an empty field falls back to the built-in default,
// so a partial theme only overrides what it names.
type Theme struct {
	Brand   string `yaml:"brand,omitempty"`    // primary accent (banner, active borders, selection)
	BrandHi string `yaml:"brand-hi,omitempty"` // brighter accent (titles, pins, summary, hits)

	// The lifecycle-state LEDs, by state name.
	Spawning  string `yaml:"spawning,omitempty"`
	Running   string `yaml:"running,omitempty"`
	Idle      string `yaml:"idle,omitempty"`
	Attention string `yaml:"attention,omitempty"`
	Exited    string `yaml:"exited,omitempty"`
	Done      string `yaml:"done,omitempty"`
	Stuck     string `yaml:"stuck,omitempty"`

	// Failed is not a lifecycle state — it is how an exited panel with a non-zero
	// exit code renders. It takes a token of its own anyway, because the whole
	// point of showing failure rather than making you infer it is that it should
	// not look like an ordinary exit.
	Failed string `yaml:"failed,omitempty"`
}

// Layout is one named group-split arrangement. With no Areas it names a built-in
// preset (tiled, main-vertical, main-horizontal, stack). A custom layout gives an
// Areas grid: Areas[r] is one row of region names, each cell naming the region
// that owns it, so a region spanning several cells repeats its name across them.
// Members fill the regions in row-major order of first appearance; members past
// the region count fold into the summary tile.
type Layout struct {
	Name  string     `yaml:"name"`
	Rows  int        `yaml:"rows,omitempty"`
	Cols  int        `yaml:"cols,omitempty"`
	Areas [][]string `yaml:"areas,omitempty"`
}

// QueueDefaults caps the task backlog: Max is the most queued (unassigned) tasks
// the backlog holds before an enqueue is refused (0 = unlimited; unset uses the
// built-in default), and Concurrency is the most tasks one work item runs at once
// (0 = unlimited).
type QueueDefaults struct {
	Max         int `yaml:"max,omitempty"`
	Concurrency int `yaml:"concurrency,omitempty"`
}

// UsageConfig configures the account usage/cost footer segment: which data source
// feeds it and how often it refreshes. The Admin-API source additionally needs an
// admin key in $BATON_ANTHROPIC_ADMIN_KEY — deliberately an env var, never this
// hand-editable file. Applied at daemon start; changing it needs a restart.
type UsageConfig struct {
	// Source selects the data source: "local" reads Claude Code's own transcripts,
	// "api" queries the Anthropic Admin usage/cost API, and "auto" (the default,
	// and the value for an empty field) prefers the api source when an admin key is
	// present, else local.
	Source string `yaml:"source,omitempty"`

	// Interval is the refresh cadence in seconds. 0 uses the built-in default (30s
	// for the local source, 60s for the api source); values below 10 are clamped up.
	Interval int `yaml:"interval,omitempty"`

	// Window is how long a billing window lasts once use opens one, as a Go
	// duration ("5h", "168h"). Empty uses the built-in default; "0" switches the countdown
	// off and reports a calendar day instead. It is configurable because plans
	// differ and the vendor can change the figure — tracking that should not need
	// a baton release. Only the local source can honour it; the api source cannot
	// see a reset at all.
	Window string `yaml:"window,omitempty"`

	// CountdownFormat is how the remaining time reads: "auto" (the default)
	// shortens to 2:14:31 under a day and widens to 3d 04:12 beyond it, while
	// "dd:hh:mm" always spells out days.
	CountdownFormat string `yaml:"countdown-format,omitempty"`

	// WarnAt and AlarmAt are the fractions of the window spent at which the footer
	// segment turns amber and then red. The point is to act before the window runs
	// out, not to watch it hit zero. 0 uses the built-in defaults (0.75 and 0.9);
	// values outside 0–1, or an alarm below the warning, fall back to them too.
	WarnAt  float64 `yaml:"warn-at,omitempty"`
	AlarmAt float64 `yaml:"alarm-at,omitempty"`
}

// The usage-window defaults, applied when the config leaves a field unset.
const (
	DefaultUsageWarnAt  = 0.75
	DefaultUsageAlarmAt = 0.90
)

// WindowDuration is the configured window length, or usage.DefaultWindow when
// the field is empty. An explicit "0" (or any non-positive duration) returns 0,
// which switches the countdown off. An unparseable value also falls back to the
// default: a typo should not silently disable the number the feature exists for.
func (u UsageConfig) WindowDuration() time.Duration {
	s := strings.TrimSpace(u.Window)
	if s == "" {
		return usage.DefaultWindow
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return usage.DefaultWindow
	}
	if d <= 0 {
		return 0
	}
	return d
}

// Thresholds are the warn and alarm fractions to colour the footer at, with the
// defaults filled in. A pair that does not describe rising pressure — either
// outside 0–1, or an alarm at or below the warning — is rejected wholesale rather
// than half-honoured, so the segment's colours always mean what they look like.
func (u UsageConfig) Thresholds() (warn, alarm float64) {
	warn, alarm = u.WarnAt, u.AlarmAt
	if warn <= 0 || warn >= 1 || alarm <= 0 || alarm > 1 || alarm <= warn {
		return DefaultUsageWarnAt, DefaultUsageAlarmAt
	}
	return warn, alarm
}

// Settings are the persisted cockpit toggles. Pointers distinguish "unset"
// (use the default) from an explicit false.
type Settings struct {
	ConfirmClose *bool `yaml:"confirm-close,omitempty"` // ask y/n before closing a panel

	// AllowNameConflict lets two work items share a name. Unset or false keeps
	// the default policy: panel titles and group names must be unique.
	AllowNameConflict *bool `yaml:"allow-name-conflict,omitempty"`

	// Bell rings the terminal when a panel enters the attention state. Unset
	// defaults to on; set false to silence the audible nudge.
	Bell *bool `yaml:"bell,omitempty"`

	// Mouse enables mouse reporting in the cockpit — the wheel scrolls the
	// scrollback and moves the dashboard selection. Unset defaults to off, so
	// the terminal's own selection and copy stay available until you opt in.
	Mouse *bool `yaml:"mouse,omitempty"`

	// UsageFooter shows the account usage/cost segment in the footer. Unset
	// defaults to on. It is the older on/off form of UsageMode, kept so a config
	// written before the segment gained views still hides what it was hiding;
	// UsageMode wins whenever it is set.
	UsageFooter *bool `yaml:"usage-footer,omitempty"`

	// UsageMode is which view the usage segment shows: "off", "window" (the
	// account's spend and the countdown to the reset) or "panel" (the focused
	// panel's spend and its share of the window). Unset defaults to the window
	// view; cycled live with the usage-footer binding (U).
	UsageMode string `yaml:"usage-mode,omitempty"`

	// DashboardPreview shows the detail pane beside the dashboard tree.
	//
	// Unset defaults to OFF, which is the opposite of what the pane did before it
	// became optional. The tree now carries the state, the directory and the task
	// that the pane used to be the only place to see, so the preview earns its
	// columns while you are watching a fleet and spends them for nothing while you
	// are reorganising one. Toggled live with the preview binding (v).
	DashboardPreview *bool `yaml:"dashboard-preview,omitempty"`

	// Keycast shows the key you just pressed, and what it did, in the footer.
	// Unset defaults to off — it is a teaching and screen-recording aid, not
	// something an everyday cockpit needs. Toggled live with the keycast
	// binding (K).
	Keycast *bool `yaml:"keycast,omitempty"`

	// Language selects the cockpit's message language — "en" or "zh-TW". Unset
	// (or a language baton does not ship) follows the environment instead:
	// $BATON_LANG, then $LC_ALL / $LC_MESSAGES / $LANG, then English.
	Language string `yaml:"language,omitempty"`

	// FoldQuiet is how many quiet panels the dashboard tolerates before it folds
	// them into one expandable "▸ N quiet" row: a panel that is merely idle, or
	// that exited cleanly, and that you have not favourited, pinned or marked.
	//
	// It exists because a fleet is mostly fine. At fifty panels the handful that
	// want something are buried under forty that do not, and scrolling past them
	// to find the one asking a question is the cost this whole feature set is
	// aimed at. Folding is by INTEREST rather than by position, and it is a fold
	// and not a filter — the row expands, so nothing is ever hidden with no way
	// back to it.
	//
	// Unset defaults to 8, which is roughly where a card grid stops fitting on
	// one screen; 0 switches folding off entirely, which is the right setting for
	// anyone who wants the dashboard to keep showing every panel it has. Below
	// the threshold nothing folds at all, so a small fleet looks exactly as it
	// does today. It is a pointer for the reason every other setting here is: an
	// explicit 0 has to survive a rewrite of the file, and an omitted int cannot
	// be told from one somebody meant.
	FoldQuiet *int `yaml:"fold-quiet,omitempty"`

	// FoldSimilar folds a group's summary tile by what its members LOOK like
	// rather than by where they sit in the group. At scale the members worth a
	// live tile are the ones that differ — after a broadcast to fifty shells the
	// useful answer is "48 identical, 2 differ, here are the 2" — and folding by
	// position instead picks the live tiles by an accident of spawn order. Unset
	// defaults to on; set false to keep the positional fold. Either way a group
	// that fits inside its visible-tile count never folds at all.
	FoldSimilar *bool `yaml:"fold-similar,omitempty"`

	// InboxDone decides whether a finished agent joins the attention inbox at
	// all — the queue's "review me" bucket, always sorted below the panels that
	// are actually asking a question.
	//
	// Unset defaults to on, because that bucket is what makes the queue
	// CLEARABLE: with it off the inbox holds only questions, failures, and
	// wedges, which is a strictly smaller promise than "here is everything that
	// wants a human, and here is how you finish with it". Set it false when you
	// run few enough agents to be watching them anyway, where a finished turn is
	// something you saw rather than something you need told.
	InboxDone *bool `yaml:"inbox-done,omitempty"`

	// InboxSnooze is how long the inbox's `-` defers a row, as a Go duration
	// ("10m", "1h"). It is applied by the COCKPIT — the absolute instant is
	// computed here and sent to the daemon — so two cockpits with different
	// values each get what they configured without the daemon holding a
	// per-client policy. Unset, unparseable, or non-positive falls back to ten
	// minutes; a snooze that silently did nothing would read as a dropped key.
	InboxSnooze string `yaml:"inbox-snooze,omitempty"`

	// Notify raises a DESKTOP notification — an OSC 9 escape written straight
	// to the terminal, the same trick the clipboard uses for OSC 52 — when
	// panels start needing a human.
	//
	// Unset defaults to OFF, unlike the bell. The bell reaches whoever is at
	// this terminal, and a terminal you are attached to is a place you chose to
	// be; a desktop toast reaches you wherever you are, and that is not a thing
	// software may assume it is welcome to do. Turn it on when the fleet is
	// large enough, or far enough away over --remote, that the bell is nobody's.
	Notify *bool `yaml:"notify,omitempty"`

	// NotifyCoalesce is how long the cockpit gathers rising edges before
	// sending one notification for all of them, as a Go duration ("30s", "2m").
	//
	// It exists because the failure mode of a fleet-scale notifier is not
	// missing an alert, it is sending forty — one toast per panel is how a
	// notification channel gets muted for good. So the first edge does not
	// fire: it opens the window, and what goes out when the window closes is
	// "3 agents need you". Unset, unparseable, or negative falls back to thirty
	// seconds; 0 sends on the next clock tick, which still coalesces whatever
	// arrived together but gives up almost all the batching.
	NotifyCoalesce string `yaml:"notify-coalesce,omitempty"`
}

// PanelDefaults configure how new panels are spawned.
type PanelDefaults struct {
	Shell string `yaml:"shell,omitempty"` // default shell binary path; empty = system shell

	// Workdir is the directory new panels run in when none is given. Empty falls
	// back to the user's home — never the directory the daemon was launched from.
	Workdir string `yaml:"workdir,omitempty"`

	// ReplayKB is the per-panel replay buffer in kibibytes — how much recent
	// output the server keeps and replays when a frontend attaches, seeding the
	// scrollback you can page through. Unset or zero uses the built-in default.
	ReplayKB int `yaml:"replay-kb,omitempty"`

	// DefaultAgent is the agent profile spawned by the new-agent action; empty
	// means the built-in "claude" profile.
	DefaultAgent string `yaml:"default-agent,omitempty"`

	// DiffCommand is the diff command run by the agent diff pop-up; empty falls
	// back to the repo's git diff.tool, then a built-in untracked-inclusive diff.
	DiffCommand string `yaml:"diff-command,omitempty"`

	// Editor is the commit editor the git menu's commit op opens (injected as
	// GIT_EDITOR); empty lets git use its own GIT_EDITOR / core.editor / EDITOR / vi
	// chain.
	Editor string `yaml:"editor,omitempty"`

	// WorktreeDir is the base directory new git-menu worktrees are created under;
	// empty defaults to a sibling "<repo>-worktrees/<branch>" of the agent's repo.
	WorktreeDir string `yaml:"worktree-dir,omitempty"`

	// Limits caps the OS resources every new panel may use — the fleet-wide floor
	// a per-agent limit would later narrow. The zero value caps nothing.
	Limits limits.Limits `yaml:"limits,omitempty"`

	// LogDir is where a panel's output log lands — on the machine the FLEET runs
	// on, since the daemon owns the PTY, which is worth knowing now that --remote
	// exists. An empty value disables logging entirely: a feature that writes
	// terminals to disk is one a user opts into by naming a directory, not one
	// that picks a default on their behalf.
	LogDir string `yaml:"log-dir,omitempty"`

	// LogMaxMB is the size at which a log rolls to "<file>.1" and starts fresh,
	// keeping the two most recent generations. 0 uses the built-in default. A
	// runaway build can produce gigabytes in minutes, and docs/LIMITS.md already
	// argues nothing should be able to take the machine with it — a log included.
	LogMaxMB int `yaml:"log-max-mb,omitempty"`

	// Restart is the fleet-wide policy for bringing a dead panel back — the floor
	// a per-agent restart would later override. Unset restarts nothing.
	Restart RestartConfig `yaml:",inline"`

	// Attention is the fleet-wide quiet ladder — done-on-quiet, done-after,
	// stuck-after — the floor a per-agent profile would later restate one line of.
	// Unset runs on the built-in defaults.
	Attention AttentionConfig `yaml:",inline"`

	// TrackCwd is how a panel's live working directory is learned: "auto" (the
	// default — the shell's own report when it makes one, the process table
	// otherwise), "osc7", "proc", or "off" to not track it at all.
	TrackCwd string `yaml:"track-cwd,omitempty"`

	// RestoreCwd is which panels a re-run puts back where they were rather than
	// where they started: "shells" (the default), "all", or "off".
	//
	// Agents are excluded by default because it is not obviously right for them. A
	// shell is wherever you last left it and going back there is the whole point;
	// an agent's task was set relative to the directory it was launched in, and one
	// that wandered into /tmp before dying should not come back in /tmp.
	RestoreCwd string `yaml:"restore-cwd,omitempty"`

	// Agents are the named agent profiles, e.g. {"claude": {command: "claude"}}.
	// A built-in "claude" profile is always available unless overridden here.
	Agents map[string]AgentProfile `yaml:"agents,omitempty"`
}

// RestartConfig is the on-disk form of a restart policy: durations as Go duration
// strings, so the file reads the way a human writes it. It is inlined into both
// the fleet-wide panel block and each agent profile, which is what makes a
// profile able to restate only the one field it changes.
type RestartConfig struct {
	// Restart is when a dead panel comes back: "never" (the default) or
	// "on-failure". "always" is deliberately not offered — see internal/restart.
	Restart string `yaml:"restart,omitempty"`

	// RestartMax is how many consecutive failures to tolerate before giving up
	// and settling the panel with the reason. 0 uses the built-in default.
	RestartMax int `yaml:"restart-max,omitempty"`

	// RestartBackoff is the base of the exponential wait between attempts, e.g.
	// "2s". Empty uses the built-in default.
	RestartBackoff string `yaml:"restart-backoff,omitempty"`

	// RestartHealthy is how long a run must last to count as a good one and reset
	// the failure counter, e.g. "30s". Empty uses the built-in default.
	RestartHealthy string `yaml:"restart-healthy,omitempty"`
}

// Policy is the parsed policy, and an error naming anything the file got wrong.
// A bad value never silently becomes a working policy: the field is left unset,
// which restarts nothing — the failure a user can see and correct, rather than
// one that quietly starts processes on their behalf.
func (r RestartConfig) Policy() (restart.Policy, error) {
	var p restart.Policy
	var err error
	if s := strings.TrimSpace(r.Restart); s != "" {
		mode, ok := restart.ParseMode(s)
		if !ok {
			err = fmt.Errorf("panel.restart %q is not a mode baton offers (never, on-failure)", s)
		} else {
			p.Mode = mode
		}
	}
	if r.RestartMax > 0 {
		p.Max = r.RestartMax
	}
	if d, derr := parseOptionalDuration(r.RestartBackoff); derr != nil {
		err = errors.Join(err, fmt.Errorf("panel.restart-backoff: %w", derr))
	} else {
		p.Backoff = d
	}
	if d, derr := parseOptionalDuration(r.RestartHealthy); derr != nil {
		err = errors.Join(err, fmt.Errorf("panel.restart-healthy: %w", derr))
	} else {
		p.Healthy = d
	}
	return p, err
}

// CwdTracking is the parsed track-cwd setting, and an error naming a value the
// file got wrong. A bad value falls back to the default rather than to "off":
// failing to learn a directory costs a convenience, not safety, so the forgiving
// direction is also the useful one.
func (p PanelDefaults) CwdTracking() (cwd.Track, error) {
	if strings.TrimSpace(p.TrackCwd) == "" {
		return cwd.Auto, nil
	}
	t, ok := cwd.ParseTrack(p.TrackCwd)
	if !ok {
		return cwd.Auto, fmt.Errorf("panel.track-cwd %q is not one of auto, osc7, proc, off", p.TrackCwd)
	}
	return t, nil
}

// CwdRestore is the parsed restore-cwd setting, and an error naming a value the
// file got wrong. A bad value falls back to the default, shells-only.
func (p PanelDefaults) CwdRestore() (cwd.Restore, error) {
	if strings.TrimSpace(p.RestoreCwd) == "" {
		return cwd.Shells, nil
	}
	r, ok := cwd.ParseRestore(p.RestoreCwd)
	if !ok {
		return cwd.Shells, fmt.Errorf("panel.restore-cwd %q is not one of shells, all, off", p.RestoreCwd)
	}
	return r, nil
}

// parseOptionalDuration reads a duration field that may be empty, which means
// "unset" rather than zero. A negative value is rejected: a wait that has already
// passed is not a wait.
func parseOptionalDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("%q is negative", s)
	}
	return d, nil
}

// AgentProfile is one named way to launch an agent: the CLI binary and its
// arguments. The panel runs it directly as the panel's process.
type AgentProfile struct {
	Command string   `yaml:"command"`        // the agent CLI binary, e.g. "claude"
	Args    []string `yaml:"args,omitempty"` // arguments passed on every spawn

	// Limits are this profile's resource caps, layered over the fleet-wide
	// panel.limits: a field set here wins, one left unset inherits. So a heavy
	// profile restates only what it changes, not the whole policy.
	Limits limits.Limits `yaml:"limits,omitempty"`

	// Restart is this profile's restart policy, layered over the fleet-wide one
	// the same way. The common case is one line — `restart: never` on an agent you
	// would rather look at yourself than have quietly re-run.
	Restart RestartConfig `yaml:",inline"`

	// Log makes this profile log from the moment it spawns. It is per-profile
	// rather than fleet-wide because "record every agent, never my shells" is the
	// case that actually comes up — and a global switch would quietly write
	// everything typed into a shell to disk.
	Log bool `yaml:"log,omitempty"`

	// LogDir overrides panel.log-dir for this profile, the same way its caps and
	// its restart policy restate only what they change.
	LogDir string `yaml:"log-dir,omitempty"`

	// Attention is this profile's quiet ladder, layered the same way. It is the
	// override that matters most in practice: how long silence means "thinking"
	// rather than "wedged" is a property of the agent, and no fleet-wide number
	// can be right for both a one-shot `--print` run and an interactive session.
	Attention AttentionConfig `yaml:",inline"`

	// Isolate runs this profile's panels inside a container: "none" (the default)
	// or "docker". It is per profile and never fleet-wide, unlike the caps and the
	// restart policy, because an isolated panel needs an IMAGE that has the right
	// toolchain — and there is no image that is right for every agent you run.
	Isolate string `yaml:"isolate,omitempty"`

	// Image is the container image the panel runs in. baton ships none: naming one
	// would mean owning a toolchain matrix it cannot maintain, so the user names an
	// image that has what their project needs. Required whenever Isolate runs.
	Image string `yaml:"image,omitempty"`

	// Mount is how much of the filesystem crosses: "workspace" (the default — the
	// panel's working directory and nothing else) or "workspace+home", which an
	// agent CLI that authenticates through a file under $HOME needs to start.
	Mount string `yaml:"mount,omitempty"`

	// Network is what the container may reach: "host" (the default), "bridge", or
	// "none".
	Network string `yaml:"network,omitempty"`

	// EnvAllow names the environment variables that cross into the container.
	// Nothing else does — there is no blanket inheritance, which is the difference
	// between an isolated panel and a panel that merely runs somewhere else.
	EnvAllow []string `yaml:"env-allow,omitempty"`

	// User is who the container runs as. Empty means the host's own uid:gid, so an
	// agent cannot leave root-owned files in the tree it was given; "root" or an
	// explicit "1000:1000" overrides that for an image that needs it.
	User string `yaml:"user,omitempty"`
}

// Isolation is the parsed isolation policy, and an error naming anything the file
// got wrong.
//
// It breaks the "report and drop" discipline the restart and cwd settings follow,
// on purpose. Dropping a malformed isolation policy would spawn the panel on the
// host, unconfined — the exact outcome the user wrote the setting to prevent —
// so a profile that MEANT to isolate and could not be understood carries the
// reason instead, and every spawn of it fails with that reason until the file is
// fixed. Keys set on a profile that never asked to isolate are inert, and are
// reported without poisoning anything: nothing was going to be confined there.
func (a AgentProfile) Isolation() (isolate.Policy, error) {
	p := isolate.Policy{
		Image:    strings.TrimSpace(a.Image),
		EnvAllow: a.EnvAllow,
		User:     strings.TrimSpace(a.User),
	}
	var err error

	intended := false
	if s := strings.TrimSpace(a.Isolate); s != "" && !strings.EqualFold(s, string(isolate.ModeNone)) {
		// Anything other than a bare "none" is an intent to isolate, INCLUDING a
		// value baton cannot parse — so a typo cannot fall through to no isolation.
		intended = true
		if mode, ok := isolate.ParseMode(s); !ok {
			err = errors.Join(err, fmt.Errorf("isolate %q is not a runtime baton offers (none, docker)", s))
		} else {
			p.Mode = mode
		}
	}
	if s := strings.TrimSpace(a.Mount); s != "" {
		m, ok := isolate.ParseMount(s)
		if !ok {
			err = errors.Join(err, fmt.Errorf("mount %q is not one of workspace, workspace+home", s))
		}
		p.Mount = m
	}
	if s := strings.TrimSpace(a.Network); s != "" {
		n, ok := isolate.ParseNetwork(s)
		if !ok {
			err = errors.Join(err, fmt.Errorf("network %q is not one of host, bridge, none", s))
		}
		p.Network = n
	}
	if intended {
		err = errors.Join(err, p.Validate()) // a missing image is a config error, not a spawn-time surprise
		if err != nil {
			p.Invalid = err.Error()
		}
		return p, err
	}
	// Not isolating: the rest of the keys do nothing, which is worth saying out
	// loud because it looks exactly like a setting that is in force.
	if p.Image != "" || p.Mount != "" || p.Network != "" || p.User != "" || len(p.EnvAllow) > 0 {
		err = errors.Join(err, errors.New("image/mount/network/user/env-allow do nothing without isolate"))
	}
	return isolate.Policy{}, err
}

// Load reads the config file. A missing file yields an empty Config and no
// error, so a first run just uses the defaults.
func Load() (Config, error) {
	var c Config
	data, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse config %s: %w", paths.ConfigFile(), err)
	}
	c.normalize()
	return c, nil
}

// LoadTUI reads the cockpit appearance file ($HOME/.baton/TUI.yaml). A missing
// file yields a zero TUIConfig and no error, so the built-in theme and the preset
// layouts apply. The caller attaches the result onto Config.TUI before the config
// is broadcast to frontends.
func LoadTUI() (TUIConfig, error) {
	var t TUIConfig
	data, err := os.ReadFile(paths.TUIConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return t, nil
		}
		return t, fmt.Errorf("read TUI config: %w", err)
	}
	if err := yaml.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse TUI config %s: %w", paths.TUIConfigFile(), err)
	}
	return t, nil
}

// normalize coerces a parsed config back into sane bounds so a hand-edited file
// cannot smuggle a nonsensical value past Load. A negative replay buffer is
// meaningless — clamp it to zero, which every consumer already reads as "use the
// built-in default" — and an unreadable resource limit falls back to "no cap"
// rather than travelling on as a value nothing downstream can parse.
func (c *Config) normalize() {
	if c.Panel.ReplayKB < 0 {
		c.Panel.ReplayKB = 0
	}
	if c.Panel.LogMaxMB < 0 {
		c.Panel.LogMaxMB = 0 // every consumer reads 0 as "use the built-in roll size"
	}
	c.Panel.Limits = c.Panel.Limits.DropInvalid()
	for name, prof := range c.Panel.Agents {
		prof.Limits = prof.Limits.DropInvalid()
		c.Panel.Agents[name] = prof
	}
}

// Save writes the config file as YAML, creating $HOME/.baton as needed. The write
// is atomic: it marshals to a sibling temp file, fsyncs it, and renames it into
// place, so a crash or full disk mid-write can never leave a truncated config.
func (c Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := paths.ConfigFile()
	if err := paths.EnsureDir(path); err != nil {
		return fmt.Errorf("prepare config dir: %w", err)
	}
	if err := paths.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
