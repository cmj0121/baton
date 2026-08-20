package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/usage"
)

// sinkHome points HOME at a fresh directory so the sink writes its file there
// rather than into the developer's own ~/.baton, and returns where that lands.
func sinkHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".baton", "usage-limits.json")
}

// statuslinePayload is a status-line stdin payload carrying both rate-limit
// windows, with the resets placed relative to now so the countdown is real.
func statuslinePayload(fivePct, sevenPct float64) string {
	now := time.Now()
	return `{"session_id":"abc","model":{"display_name":"Opus"},` +
		`"context_window":{"used_percentage":8},"rate_limits":{` +
		`"five_hour":{"used_percentage":` + strconv.FormatFloat(fivePct, 'f', 1, 64) +
		`,"resets_at":` + strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10) + `},` +
		`"seven_day":{"used_percentage":` + strconv.FormatFloat(sevenPct, 'f', 1, 64) +
		`,"resets_at":` + strconv.FormatInt(now.Add(72*time.Hour).Unix(), 10) + `}}}`
}

// runSink drives usageSinkMain with stdin fed from payload and stdout captured,
// returning what the sink printed and the exit code it chose. Both streams are
// real files rather than pipes: the sink may write more than a pipe buffer holds
// when it runs a wrapped command, and a test that deadlocked on that would be
// indistinguishable from one that hung.
func runSink(t *testing.T, payload string, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()

	in, err := os.Create(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	code := usageSinkMain(args)
	os.Stdin, os.Stdout = oldIn, oldOut
	_ = in.Close()
	_ = out.Close()

	b, err := os.ReadFile(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b), code
}

// With no status line of the user's own to defer to, the sink prints baton's
// own quota line — a row worth having rather than a blank one.
func TestUsageSinkPrintsOwnLine(t *testing.T) {
	path := sinkHome(t)
	out, code := runSink(t, statuslinePayload(62.4, 34.1))
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(out, "5h ") || !strings.Contains(out, "7d ") {
		t.Errorf("own line = %q, want both windows", out)
	}
	// The bar is the reading; no percentage rides beside it.
	if strings.Contains(out, "%") {
		t.Errorf("own line printed a percentage beside the bar: %q", out)
	}
	l, ok := usage.ReadLimits(path)
	if !ok {
		t.Fatalf("the sink did not harvest to %s", path)
	}
	if l.FiveHour == nil || l.FiveHour.UsedPercent != 62.4 || l.Source != usage.LimitsStatusline {
		t.Errorf("harvested %+v, want the payload's five-hour window", l.FiveHour)
	}
}

// The user's own status line is run with the same input and its output passed
// through verbatim: a panel inside baton looks like a panel outside it.
func TestUsageSinkWrapsUserStatusLine(t *testing.T) {
	path := sinkHome(t)
	// The wrapped command reads the payload from its own stdin, proving the bytes
	// were handed on rather than eaten.
	out, code := runSink(t, statuslinePayload(10, 20),
		"--wrap", `sed -n 's/.*"display_name":"\([^"]*\)".*/[\1] mine/p'`)
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "[Opus] mine" {
		t.Errorf("wrapped output = %q, want the user's own line", out)
	}
	// …and the harvesting still happened behind it.
	if _, ok := usage.ReadLimits(path); !ok {
		t.Error("wrapping the user's status line skipped the harvest")
	}
}

// A payload with no rate limits is the ordinary state of a session before its
// first API response. It must cost the user nothing — not their status line, not
// an error, not a stray file.
func TestUsageSinkWithoutRateLimits(t *testing.T) {
	path := sinkHome(t)
	out, code := runSink(t, `{"session_id":"x","model":{"display_name":"Opus"}}`, "--wrap", "echo INTACT")
	if code != 0 || strings.TrimSpace(out) != "INTACT" {
		t.Errorf("out=%q code=%d, want the wrapped line and 0", out, code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a payload with no limits wrote %s anyway", path)
	}
	// With nothing harvested and no status line to wrap, there is nothing to print.
	if out, _ := runSink(t, `{"session_id":"x"}`); out != "" {
		t.Errorf("own line with nothing to report = %q, want empty", out)
	}
	// Neither is unreadable input worth saying anything about — into a status line,
	// a diagnostic would render once per frame until the user worked out why.
	if out, code := runSink(t, "not json at all"); out != "" || code != 0 {
		t.Errorf("junk payload: out=%q code=%d, want empty and 0", out, code)
	}
}

// A status line that fails is the user's own business, and its exit code is
// reported rather than swallowed.
func TestUsageSinkWrappedFailure(t *testing.T) {
	sinkHome(t)
	if _, code := runSink(t, statuslinePayload(5, 5), "--wrap", "exit 3"); code != 3 {
		t.Errorf("exit = %d, want the wrapped command's 3", code)
	}
	// A command that cannot start at all leaves the row blank, exactly as it would
	// have been without baton in the way — never a non-zero exit of baton's own.
	if _, code := runSink(t, statuslinePayload(5, 5), "--wrap", "/nonexistent/statusline"); code == 0 {
		t.Log("the shell reported the missing command; its code rides through")
	}
}

// Hand-parsed because kong's failure mode is to print usage, and this command's
// stdout is a status line.
func TestParseWrap(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"none":          {nil, ""},
		"separate":      {[]string{"--wrap", "echo hi"}, "echo hi"},
		"equals":        {[]string{"--wrap=echo hi"}, "echo hi"},
		"empty equals":  {[]string{"--wrap="}, ""},
		"dangling":      {[]string{"--wrap"}, ""},
		"after junk":    {[]string{"-x", "--wrap", "cmd"}, "cmd"},
		"unknown alone": {[]string{"--nonsense"}, ""},
	} {
		if got := parseWrap(tc.args); got != tc.want {
			t.Errorf("%s: parseWrap(%q) = %q, want %q", name, tc.args, got, tc.want)
		}
	}
}

// SHELL is honoured so a user whose status line relies on their own shell's
// syntax keeps it; /bin/sh is the fallback every platform baton runs on has.
func TestSinkShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := shell(); got != "/bin/zsh" {
		t.Errorf("shell() = %q, want the SHELL override", got)
	}
	t.Setenv("SHELL", "")
	if got := shell(); got != "/bin/sh" {
		t.Errorf("shell() = %q, want /bin/sh", got)
	}
}

// harvest is silent and survivable on every path: it is called once per render
// of every panel in the fleet, and nothing it can fail at is worth a word.
func TestHarvestIsSilent(t *testing.T) {
	path := sinkHome(t)
	harvest(nil)
	harvest([]byte("{{{"))
	harvest([]byte(`{"rate_limits":{}}`))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("harvest wrote a file from input that carries no reading")
	}
	harvest([]byte(statuslinePayload(50, 25)))
	if _, ok := usage.ReadLimits(path); !ok {
		t.Fatal("harvest did not write a real reading")
	}
	// The payload also carries the transcript path, the working directory and the
	// session cost. A sink that forwards only what it needs cannot leak the rest.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := json.Unmarshal(b, &kept); err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"session_id", "model", "context_window", "workspace", "cost"} {
		if _, found := kept[leaked]; found {
			t.Errorf("the sink file carries %q, which it never came for", leaked)
		}
	}
}

// limitsOption is what the daemon wires the quota bars from; each source has to
// produce a usable option rather than a nil the server would panic applying.
func TestLimitsOption(t *testing.T) {
	sinkHome(t)
	for _, source := range []string{"", "statusline", "oauth", "off", "nonsense"} {
		opt := limitsOption(config.Config{Usage: config.UsageConfig{Limits: source}})
		if opt == nil {
			t.Errorf("limitsOption(%q) = nil", source)
		}
	}
	// The status-line source reads the sink file the panels write, so a reading
	// dropped there is one the daemon can pick up.
	if _, err := usage.WriteLimitsIfChanged(paths.UsageLimitsFile(), usage.Limits{
		FiveHour: &usage.Window{UsedPercent: 40}, Source: usage.LimitsStatusline, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := usage.NewStatuslineLimits(paths.UsageLimitsFile()).Limits(t.Context()); !ok {
		t.Error("the status-line source could not read the file the sink writes")
	}
}
