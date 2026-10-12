package server

import (
	"math"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// commandVendor is the agent a process is, taken from the command's base name.
// A profile called "reviewer" that runs claude is claude: the quota belongs to
// the account the binary spends, not to the name the fleet gave the profile.
func commandVendor(command string) string {
	base := filepath.Base(strings.TrimSpace(command))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return base
}

// panelVendorLocked is the vendor of a standing panel, from the command it was
// spawned with. An empty command is a panel we cannot attribute, and a pause
// that cannot name a vendor does not fire. Caller holds s.mu.
func (s *Server) panelVendorLocked(id string) string {
	spec, ok := s.specs[id]
	if !ok {
		return ""
	}
	return commandVendor(spec.Command)
}

// quotaHoldsLocked reports whether vendor's own remaining quota is under
// pauseBelow. Off, an unknown vendor, and a vendor with no reading all return
// false: no number is not a reason to stop the queue. Caller holds s.mu.
func (s *Server) quotaHoldsLocked(vendor string) bool {
	if s.pauseBelow <= 0 || s.pauseBelow > 1 || vendor == "" {
		return false
	}
	rem, ok := s.vendorRemainingLocked(vendor)
	if !ok {
		return false
	}
	return rem < s.pauseBelow
}

// vendorRemainingLocked is the smallest remaining share of the windows that
// belong to vendor. Claude uses its account 5h and 7d only — not the per-model
// opus/sonnet ceilings. Grok uses the 7d window on its own reading only. A
// spend window labelled "window" is how far through a measured interval baton
// is, not a ceiling, and it pauses nobody. No window means no reading.
// Caller holds s.mu.
func (s *Server) vendorRemainingLocked(vendor string) (float64, bool) {
	switch vendor {
	case usage.LimitsVendor:
		return claudeRemaining(s.limitsInfo)
	case "grok":
		return grokRemaining(s.usageInfo)
	default:
		return 0, false
	}
}

func claudeRemaining(info *proto.LimitsInfo) (float64, bool) {
	if info == nil {
		return 0, false
	}
	var rem float64
	var ok bool
	for _, w := range []*proto.LimitWindow{info.FiveHour, info.SevenDay} {
		if w == nil {
			continue
		}
		r := remaining(w.UsedPercent)
		if !ok || r < rem {
			rem, ok = r, true
		}
	}
	return rem, ok
}

func grokRemaining(info *proto.UsageInfo) (float64, bool) {
	if info == nil {
		return 0, false
	}
	for i := range info.Vendors {
		v := &info.Vendors[i]
		if v.Vendor != "grok" || v.State != string(usage.VendorReading) {
			continue
		}
		for _, w := range v.Windows {
			if w.Label != usage.WindowWeek {
				continue
			}
			return remaining(w.UsedPercent), true
		}
		return 0, false
	}
	return 0, false
}

// remaining is the share still left of a 0–100 used percentage, clamped so a
// vendor that reports past 100% is empty rather than negative.
//
// The result is snapped to the nearest millionth. 80/100 is not 0.2 in
// float64 (it lands a hair under), and a line of 0.2 must not pause a window
// that is exactly 80% used. A millionth is finer than any tenth of a percent
// a vendor publishes, so a reading that is actually under the line still holds.
func remaining(usedPercent float64) float64 {
	f := usedPercent / 100
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return math.Round((1-f)*1e6) / 1e6
}

// noteQuotaHoldLocked logs the moment a vendor's queue pause begins or ends.
// The first observation of "not held" is not a resume. Caller holds s.mu.
func (s *Server) noteQuotaHoldLocked(vendor string, held bool) {
	if vendor == "" {
		return
	}
	if s.quotaHold == nil {
		s.quotaHold = map[string]bool{}
	}
	if prev, ok := s.quotaHold[vendor]; ok && prev == held {
		return
	}
	if _, seen := s.quotaHold[vendor]; !seen && !held {
		s.quotaHold[vendor] = false
		return
	}
	s.quotaHold[vendor] = held
	if held {
		log.Info().Str("vendor", vendor).Float64("pause_below", s.pauseBelow).
			Msg("queue paused: vendor remaining quota is below the line")
		return
	}
	log.Info().Str("vendor", vendor).Msg("queue resumed: vendor remaining quota is back above the line")
}

// scheduleAgentLocked picks who may take queued work in group.
//
// An unheld free agent wins, even when a held agent sits earlier in the fleet:
// pausing claude must not stop grok. When the only free agents are held, the
// second result is true and the caller must not spawn in their place. No free
// agent at all returns "", false, and spawn-on-demand may run — subject to
// that spawn's own vendor. Caller holds s.mu.
func (s *Server) scheduleAgentLocked(group string) (id string, held bool) {
	var heldID string
	for i := range s.panels {
		p := &s.panels[i]
		if !s.agentFreeLocked(p, group) {
			continue
		}
		vendor := s.panelVendorLocked(p.ID)
		if s.quotaHoldsLocked(vendor) {
			s.noteQuotaHoldLocked(vendor, true)
			if heldID == "" {
				heldID = p.ID
			}
			continue
		}
		s.noteQuotaHoldLocked(vendor, false)
		return p.ID, false
	}
	if heldID != "" {
		return heldID, true
	}
	return "", false
}
