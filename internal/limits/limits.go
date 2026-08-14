// Package limits is baton's resource-cap domain: what a cap on a panel means,
// how it is spelled, and how two layers of them combine. It is deliberately a
// neutral package rather than part of the config file format or of the
// enforcement backend, because both ends need the same vocabulary — the YAML
// layer reads caps into it, the cgroup layer writes the kernel from it — and
// neither should have to depend on the other to do so.
package limits

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Unlimited is the explicit "drop the inherited cap" value any field may carry.
// It is spelled out because an empty field already means something else —
// "inherit whatever the layer above set" — and a numeric type could not tell the
// two apart. That is why every limit below is a string, not an int.
const Unlimited = "unlimited"

// Limits caps the OS resources a panel's process tree may use. Each field is a
// quantity string with three states: absent (inherit the layer above), a value
// (cap at that), or Unlimited (explicitly uncapped). The zero Limits therefore
// means "inherit everything", which is what an un-configured baton runs with.
type Limits struct {
	// CPUs is the CPU-core allowance for the whole panel tree, e.g. "2" or "1.5".
	CPUs string `yaml:"cpus,omitempty"`

	// Memory is the hard memory cap, e.g. "4Gi". Reaching it kills the process.
	Memory string `yaml:"memory,omitempty"`

	// MemoryHigh is the throttle-before-kill watermark: past it the tree is
	// reclaimed hard but stays alive, so it is the knob to reach for first.
	MemoryHigh string `yaml:"memory-high,omitempty"`

	// Pids is the most processes and threads the tree may hold — the fork-bomb cap.
	Pids string `yaml:"pids,omitempty"`

	// NOFile is the open-file-descriptor cap for each process in the tree.
	NOFile string `yaml:"nofile,omitempty"`
}

// field is one cap: its name in the config and in reports, a pointer to its slot
// on a Limits, and the parser that decides whether its value is readable.
type field struct {
	name  string
	value *string
	parse func(string) error
}

// fields is the one place the caps are enumerated. Every method below walks it,
// so a new cap is a single entry rather than a matching edit in each of them —
// and Validate and DropInvalid cannot drift on what "unreadable" means, since
// they consult the same parser.
func (l *Limits) fields() []field {
	return []field{
		{"cpus", &l.CPUs, func(s string) error { _, _, err := ParseCPUs(s); return err }},
		{"memory", &l.Memory, func(s string) error { _, _, err := ParseBytes(s); return err }},
		{"memory-high", &l.MemoryHigh, func(s string) error { _, _, err := ParseBytes(s); return err }},
		{"pids", &l.Pids, func(s string) error { _, _, err := ParseCount(s); return err }},
		{"nofile", &l.NOFile, func(s string) error { _, _, err := ParseCount(s); return err }},
	}
}

// IsZero reports whether the limits set nothing at all, so a caller can skip the
// whole policy rather than reason about five empty fields.
func (l Limits) IsZero() bool { return l == Limits{} }

// Merge layers over onto l field by field: a field set on over wins, one left
// empty keeps l's. It is how a per-agent profile narrows (or widens) the
// fleet-wide default while restating only the fields it actually changes — and
// why Unlimited has to be a value rather than an empty field, since an empty one
// inherits instead of lifting the cap.
func (l Limits) Merge(over Limits) Limits {
	mine, theirs := l.fields(), over.fields()
	for i := range mine {
		if *theirs[i].value != "" {
			*mine[i].value = *theirs[i].value
		}
	}
	return l
}

// Fields renders the limits as a map of just the caps that are set — the shape
// the event bus and the structured logs carry. An unset field is absent rather
// than an empty string, so a consumer reads "no cap" as a missing key; a policy
// that caps nothing renders as nil.
func (l Limits) Fields() map[string]any {
	var out map[string]any
	for _, f := range l.fields() {
		if *f.value == "" {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(l.fields()))
		}
		out[f.name] = *f.value
	}
	return out
}

// Validate returns the first field that carries a quantity this package cannot
// read, naming the field so the message can be shown as-is. It is what the
// cockpit calls before saving a typed value, so a limit written through the UI is
// always readable back.
func (l Limits) Validate() error {
	for _, f := range l.fields() {
		if err := f.parse(*f.value); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

// DropInvalid blanks any field a hand-edited file left unreadable, so a typo in
// the config degrades to "inherit" rather than wedging the daemon on load. The
// cockpit's own edits never reach here unreadable — Validate gates those.
func (l Limits) DropInvalid() Limits {
	for _, f := range l.fields() {
		if f.parse(*f.value) != nil {
			*f.value = ""
		}
	}
	return l
}

// Uncapped reports whether a quantity asks for no cap at all — an empty field
// (inherit) or Unlimited (explicitly uncapped) — and returns the value with its
// surrounding space trimmed. Both the parsers and the frontends that label a cap
// need this exact test, so it has one spelling.
func Uncapped(s string) (trimmed string, uncapped bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, Unlimited) {
		return "", true
	}
	return s, false
}

// ParseCPUs reads a CPU-core allowance, e.g. "2" or "1.5". An uncapped field is
// not a cap at all: limited is false and cores is zero, so a caller can never
// mistake "uncapped" for "zero cores".
func ParseCPUs(s string) (cores float64, limited bool, err error) {
	s, uncapped := Uncapped(s)
	if uncapped {
		return 0, false, nil
	}
	n, err := positive(s, "a number of cores")
	if err != nil {
		return 0, false, fmt.Errorf("%q is not %w", s, err)
	}
	return n, true, nil
}

// ParseCount reads a plain whole-number allowance (pids, file descriptors). Like
// ParseCPUs, an uncapped field reports limited false.
func ParseCount(s string) (n int64, limited bool, err error) {
	s, uncapped := Uncapped(s)
	if uncapped {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%q is not a whole number", s)
	}
	if v <= 0 {
		return 0, false, fmt.Errorf("%q is not greater than zero", s)
	}
	return v, true, nil
}

// byteUnits are the size suffixes ParseBytes accepts, longest first so "Gi" is
// matched before "G". The "i" forms are binary (1024-based) and the bare letters
// decimal (1000-based), the same split Kubernetes and systemd use.
var byteUnits = []struct {
	suffix string
	mult   float64
}{
	{"KI", 1 << 10}, {"MI", 1 << 20}, {"GI", 1 << 30}, {"TI", 1 << 40},
	{"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
}

// ParseBytes reads a memory quantity — "4Gi", "512Mi", "1.5G", or a plain byte
// count — and returns it in bytes. A trailing "B" is accepted ("4GiB"), and the
// unit is case-insensitive. An uncapped field reports limited false.
func ParseBytes(s string) (bytes int64, limited bool, err error) {
	raw, uncapped := Uncapped(s)
	if uncapped {
		return 0, false, nil
	}
	u := strings.TrimSuffix(strings.ToUpper(raw), "B")
	mult := 1.0
	for _, unit := range byteUnits {
		if rest, ok := strings.CutSuffix(u, unit.suffix); ok {
			u, mult = rest, unit.mult
			break
		}
	}
	n, err := positive(strings.TrimSpace(u), "a size (try 4Gi, 512Mi)")
	if err != nil {
		return 0, false, fmt.Errorf("%q is not %w", raw, err)
	}
	if total := n * mult; total <= math.MaxInt64 {
		return int64(total), true, nil
	}
	return 0, false, fmt.Errorf("%q is too large", raw)
}

// positive reads a finite number greater than zero, describing what was expected
// when it is not one. It is the check every quantity shares; want reads as the
// tail of "is not …", so it names the shape rather than the failure.
func positive(s, want string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, errors.New(want)
	}
	if n <= 0 {
		return 0, errors.New("greater than zero")
	}
	return n, nil
}
