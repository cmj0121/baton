package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// statusReply is the decoded score.status payload. Named for what it is rather
// than for the subsystem, so it cannot be mistaken for the server's own
// scoreState — and so `status`, the helper below, stays reachable from every
// test in the package.
type statusReply struct {
	Enabled       bool       `json:"enabled"`
	Available     bool       `json:"available"`
	Unlocked      bool       `json:"unlocked"`
	Reason        string     `json:"reason"`
	Entries       int        `json:"entries"`
	Rendered      int        `json:"rendered"`
	Oversized     int        `json:"oversized"`
	BlockFull     bool       `json:"block_full"`
	PromoteAt     int        `json:"promote_at"`
	UserSignalsAt int        `json:"user_signals_at"`
	WorkingSet    int        `json:"working_set"`
	Rank          score.Rank `json:"rank"`
	Dir           string     `json:"dir"`
}

// status runs score.status against s and decodes the reply.
func status(t *testing.T, s *Server) statusReply {
	t.Helper()
	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.status"})
	msg := reply(t, cc)
	var got statusReply
	if msg.Type != "score" || json.Unmarshal(msg.Score, &got) != nil {
		t.Fatalf("score.status must answer a score object, got %+v", msg)
	}
	return got
}

// scoreStore opens a store in a fresh directory and closes it when the test
// ends, so a test that wants one is three words rather than six lines.
func scoreStore(t *testing.T) (*score.Store, string) {
	t.Helper()
	return scoreStoreTuned(t, score.Policy{})
}

// scoreStoreTuned is scoreStore for a test whose subject IS the tuning. Open
// clamps a zero policy onto the package defaults, so the two differ only in what
// the operator is taken to have set.
func scoreStoreTuned(t *testing.T, p score.Policy) (*score.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := score.Open(dir, p)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	return st, dir
}

// TestScoreStatusDistinguishesOffFromUnavailable is the operator's only runtime
// way to learn their fleet memory is not running, and why. BATON_SOCK is the
// documented way to run a second fleet, both fleets default to $HOME/.baton, so
// "another daemon holds the directory" is the ordinary case — and answering it
// with the same "disabled" the config knob produces leaves that operator with an
// empty memory and nothing to explain it.
func TestScoreStatusDistinguishesOffFromUnavailable(t *testing.T) {
	st, dir := scoreStore(t)

	held := "score: /home/u/.baton/score.lock is held by another baton daemon; one writer per score directory"
	cases := []struct {
		name   string
		store  *score.Store
		on     bool
		reason string
		want   statusReply
	}{
		{"running", st, true, "", statusReply{
			Enabled: true, Available: true, Entries: 0, Rendered: 0,
			PromoteAt: 3, UserSignalsAt: 2, WorkingSet: 7,
			Rank: score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}, Dir: dir,
		}},
		{"held by another daemon", nil, true, held, statusReply{Enabled: true, Reason: held}},
		{"switched off", nil, false, "score is switched off in the config (score.enabled: false)",
			statusReply{Reason: "score is switched off in the config (score.enabled: false)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := scoreServer(tc.store)
			WithScore(ScoreState{Store: tc.store, Enabled: tc.on, Reason: tc.reason})(s)

			if got := status(t, s); got != tc.want {
				t.Fatalf("status = %+v, want %+v", got, tc.want)
			}

			// A refusal says the same thing the status does, so an agent's error
			// and an operator's status never disagree about why.
			cc := conn("")
			s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "anything"})
			msg := reply(t, cc)
			switch {
			case tc.store != nil:
				if msg.Type != "score" {
					t.Fatalf("submit to a running store answered %+v", msg)
				}
			case msg.Type != "error" || msg.Error != tc.reason:
				t.Fatalf("submit refusal = %+v, want the status reason %q", msg, tc.reason)
			}
		})
	}
}

// TestReconcileFailureIsLatched keeps a persistent failure from logging once per
// dispatch forever: the read error returns before the store's fingerprint is
// touched, so the gate stays armed and every dispatch retries. What the operator
// needs is the transition in and the transition out.
func TestReconcileFailureIsLatched(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("still readable", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)

	// score.md as a directory: every read of it fails, for as long as it lasts.
	path := filepath.Join(dir, "score.md")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir over score.md: %v", err)
	}

	for range 5 {
		s.scoreView(score.Context{})
	}
	if !s.scoreState.failing.Load() {
		t.Fatal("a failing reconcile did not latch, so it logs on every dispatch")
	}
	// The last view read is still served — a stale brief beats no brief.
	cc := conn("")
	s.scoreList(cc, proto.Command{Action: "score.list"})
	if got := string(reply(t, cc).Score); !strings.Contains(got, "still readable") {
		t.Fatalf("score.list = %s, want the last view the store did read", got)
	}
	// And the operator is told, rather than being shown a healthy status while
	// their edits sit inert (invariant I8).
	if got := status(t, s); !got.Available || got.Reason == "" {
		t.Fatalf("status = %+v, want an available store whose file cannot be read", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("# fixed\n"), 0o600); err != nil {
		t.Fatalf("restore score.md: %v", err)
	}
	s.scoreView(score.Context{})
	if s.scoreState.failing.Load() {
		t.Fatal("the latch did not clear, so recovery would never be reported")
	}
	if got := status(t, s); got.Reason != "" {
		t.Fatalf("status = %+v, want the reason cleared with the latch", got)
	}
}

// TestScoreStatusReportsWithheldLines is invariant I8 on the entry weight cap.
// A line too long to inject is withheld from score.list as well as from every
// brief, so without a count on the wire the operator's own entry is invisible
// everywhere except the file they typed it into — and the entries/rendered gap
// reads exactly like an ordinary render-limit truncation.
func TestScoreStatusReportsWithheldLines(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("a normal note", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)

	if got := status(t, s); got.Entries != 1 || got.Rendered != 1 || got.Oversized != 0 {
		t.Fatalf("status = %+v, want one entry, injected, nothing withheld", got)
	}

	// The operator adds a line far past the weight cap.
	editScoreMD(t, dir, readScoreMD(t, dir)+"- "+strings.Repeat("x", 50_000)+"\n")

	got := status(t, s)
	switch {
	case got.Entries != 2:
		t.Fatalf("status = %+v, want the long line counted as an entry", got)
	case got.Rendered != 1:
		t.Fatalf("status = %+v, want it withheld from the brief", got)
	case got.Oversized != 1:
		t.Fatalf("status = %+v, want the gap explained as a weight cap, not the working-set budget", got)
	}
}

// TestScoreStatusStopsClaimingHealthWhenWritesStopLanding is #38's invariant I8
// against the fourth state its three do not name.
//
// A store on a read-only mount READS perfectly: it opened, the boot replayed,
// every view reconciles. score.status answered `available: true, entries: N`
// with an empty reason while every submission was refused — the one reading of
// this subsystem that is not merely incomplete but false. available still means
// "a store is running", because it does; the reason field is where the states
// the enabled/available pair cannot express already live.
//
// Both directions, and the second is the reason this is a report rather than a
// probe: the latch clears itself when writes land again, so a mount that comes
// back needs no restart to stop being reported as broken.
func TestScoreStatusStopsClaimingHealthWhenWritesStopLanding(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)
	if _, _, err := st.Submit("the fleet keeps the build green", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	if got := status(t, s); !got.Available || got.Reason != "" {
		t.Fatalf("status = %+v, want a healthy store reporting nothing wrong", got)
	}

	// The LOG unwritable, which is what a read-only mount does to it. The
	// directory's own write bit is not enough: the log already exists, and
	// appending to an existing file needs no permission on the directory holding
	// it — which is precisely how a store keeps reading and stops recording.
	events := filepath.Join(dir, "score-events.jsonl")
	restore := unwritable(t, events, func() error {
		f, err := os.OpenFile(events, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	})

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "and it asks before it force-pushes"})
	if msg := reply(t, cc); msg.Type != "error" {
		t.Fatalf("submit into a read-only store answered %+v, want a refusal", msg)
	}
	got := status(t, s)
	// The entries are still there and still served, which is exactly why the
	// old reply was believable.
	if !got.Available || got.Entries == 0 {
		t.Fatalf("status = %+v, want the store still readable and serving what it holds", got)
	}
	if got.Reason == "" {
		t.Errorf("status = %+v: every write is failing and the operator is told nothing, which is "+
			"the one reading of this subsystem that is false rather than merely partial", got)
	}
	if !strings.Contains(got.Reason, "write") {
		t.Errorf("reason = %q, want it to name what stopped working", got.Reason)
	}

	// The mount comes back. A probe at open could never learn this, and a latch
	// that needed a restart to clear would be the next thing lying.
	restore()
	cc = conn("")
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "and it asks before it force-pushes"})
	if msg := reply(t, cc); msg.Type != "score" {
		t.Fatalf("submit into a restored store answered %+v", msg)
	}
	if got := status(t, s); got.Reason != "" {
		t.Errorf("status = %+v, want the store to stop reporting a failure it has recovered from", got)
	}
}

// readScoreMD returns the current score.md, so a test can append to it the way
// an operator would rather than replacing what the store wrote.
func readScoreMD(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	return string(data)
}

// TestASubmissionTheStoreCouldNotRecordIsLogged closes the observability gap
// #46 recorded: a submission that fails reaches the CLIENT and nothing else.
//
// The client is the wrong audience for it. A submission fails for one of two
// reasons — text that sanitised away to nothing, which is the submitter's to fix
// — or a durable write that did not land, which is a full disk, a read-only
// mount, or a directory that went away under a running daemon. The second is the
// operator's problem, and the only party told about it was an agent with no
// reason to keep the reply and every reason to retry past it. A fleet could
// submit into a broken store all day with nothing in the daemon log to show for
// it, while invariant I8 says the operator must not have to read the event log
// to find out their memory is not working — which is doubly true when the event
// log is what could not be written.
//
// THREE directions, because the line has two silent halves and only one
// speaking one. A durable write that did not land logs; a submission that lands
// logs no warning, because a Warn on every accepted note would bury the one that
// matters; and a submission the store refused for its own TEXT logs nothing
// either, because that refusal is the submitter's and every panel on the fleet
// can produce it on demand.
func TestASubmissionTheStoreCouldNotRecordIsLogged(t *testing.T) {
	t.Run("a failed submission is logged", func(t *testing.T) {
		st, dir := scoreStore(t)
		s, _, _ := scoreServer(st)
		// The DIRECTORY unwritable, so the log's first durable append cannot even
		// create the file — which is where a full or read-only disk leaves it.
		unwritable(t, dir, func() error {
			probe := filepath.Join(dir, "probe")
			f, err := os.Create(probe)
			if err != nil {
				return err
			}
			_ = f.Close()
			return os.Remove(probe)
		})

		logged := captureLog(t)
		cc := conn("")
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "the fleet keeps the build green"})
		if msg := reply(t, cc); msg.Type != "error" {
			t.Fatalf("submit into an unwritable directory answered %+v, want the store's refusal", msg)
		}
		got := logged()
		if !strings.Contains(got, "score could not record a submission") {
			t.Errorf("the daemon said nothing about a submission it could not store:\n%s", got)
		}
		if !strings.Contains(got, dir) {
			t.Errorf("the log line does not name the directory that failed:\n%s", got)
		}
	})

	// The distinction the comment on scoreSubmit draws, asserted. It was drawn
	// and asserted nowhere: two hundred blank submissions produced two hundred
	// lines claiming the store was broken while it was healthy, so the line an
	// operator greps for a dead disk was the line an agent got for sending
	// spaces.
	t.Run("a submission the store refused for its own text is not the operator's problem", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			prompt string
		}{
			{"whitespace that sanitises away to nothing", "   \t  "},
			{"text past the entry cap", strings.Repeat("x", 4000)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				st, _ := scoreStore(t)
				s, _, _ := scoreServer(st)

				logged := captureLog(t)
				cc := conn("")
				s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: tc.prompt})
				if msg := reply(t, cc); msg.Type != "error" {
					t.Fatalf("the store accepted %q: %+v", tc.name, msg)
				}
				if got := logged(); strings.Contains(got, "score could not record a submission") {
					t.Errorf("a healthy store was reported broken because a submitter sent bad text:\n%s", got)
				}
			})
		}
	})

	t.Run("a submission that lands logs no warning", func(t *testing.T) {
		st, _ := scoreStore(t)
		s, _, _ := scoreServer(st)

		logged := captureLog(t)
		cc := conn("")
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "the fleet keeps the build green"})
		if msg := reply(t, cc); msg.Type != "score" {
			t.Fatalf("submit to a healthy store answered %+v", msg)
		}
		if got := logged(); strings.Contains(got, "could not record") {
			t.Errorf("a submission that landed produced a failure line:\n%s", got)
		}
	})
}

// unwritable takes the write bits off path for the rest of the test, then asks
// the filesystem whether that took by running probe — skipping when it did not.
//
// Both halves matter and the second is the one worth stating. Whether a chmod
// binds is a question about the USER, not about the store: root ignores the
// bits and so do a few filesystems. Asking it of the filesystem BEFORE the
// command under test runs is what keeps the skip honest — deciding it from the
// command's own reply is what let a mutation that deleted a whole error branch
// report itself as a skipped test, and a skip that fires when the code is broken
// is worse than no test at all.
//
// probe is the caller's because the two things this is used on are different
// operations: appending to an existing log needs no permission on the directory
// holding it, which is precisely how a store keeps reading and stops recording.
//
// The undo is returned as well as deferred, so a test can watch a write fail and
// then watch the same write succeed once the mount comes back — which is the
// half a latch that needed a restart to clear would fail. It matches
// score.unwritable, whose shape this is.
func unwritable(t *testing.T, path string, probe func() error) (restore func()) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	mode := fi.Mode().Perm()
	if err := os.Chmod(path, mode&^0o222); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	restore = func() { _ = os.Chmod(path, mode) }
	t.Cleanup(restore)
	if probe() == nil {
		t.Skipf("this user still writes to %s with no write bit; the test needs an unprivileged one", path)
	}
	return restore
}
