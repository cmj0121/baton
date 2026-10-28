package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

const (
	pasteOn  = "\x1b[?2004h"
	pasteOff = "\x1b[?2004l"
)

// pasteModeOf reads the tracked state the way typeLine does, plus whether the
// panel has an entry at all — unknown and off type the same, but a reset is
// about the entry being gone.
func pasteModeOf(s *Server, id string) (on, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	on, known = s.pasteMode[id]
	return on, known
}

// The fix itself: an agent that asked for bracketed paste gets its text as one
// paste, and the submit after the gap as its own write.
func TestTypeLinePasteModeWraps(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 0
	s.fanOutput("1", []byte("banner"+pasteOn+"> "), 0)

	if !s.typeLine("1", true, []byte("do it"), "\r") {
		t.Fatal("a pasted line must still report submitted")
	}
	equalWrites(t, w.got(), []string{"1:\x1b[200~do it\x1b[201~", "1:\r"})
}

// Off and never-set both type what they always did.
func TestTypeLinePasteModeOffOrUnknownRaw(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"unknown", "plain prompt> "},
		{"off", pasteOn + "x" + pasteOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
			s.submitGap = 0
			s.fanOutput("1", []byte(tc.out), 0)

			s.typeLine("1", true, []byte("do it"), "\r")
			equalWrites(t, w.got(), []string{"1:do it", "1:\r"})
		})
	}
}

// A shell asks for bracketed paste before every prompt; its one-write line is
// left exactly as it was.
func TestTypeLinePasteModeShellRaw(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Shell, Pid: 10})
	s.submitGap = 0
	s.fanOutput("1", []byte(pasteOn+"$ "), 0)

	s.typeLine("1", false, []byte("ls"), "\n")
	equalWrites(t, w.got(), []string{"1:ls\n"})
}

func TestAsPasteStripsMarkers(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"end", "a\x1b[201~rm -rf\r", "arm -rf\r"},
		{"start", "a\x1b[200~b", "ab"},
		// Removing the inner marker joins its neighbours into the other one.
		{"joined", "\x1b[20\x1b[201~0~x", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := string(asPaste([]byte(tc.in))), "\x1b[200~"+tc.want+"\x1b[201~"; got != want {
				t.Fatalf("asPaste(%q) = %q, want %q", tc.in, got, want)
			}
		})
	}
}

// The mode is read from output whatever track-cwd says: the cwd reader returns
// early when it is off, and the paste reader must not be behind it.
func TestPasteModeTrackedWithCwdOff(t *testing.T) {
	s := cwdServer(cwd.Off, cwd.Shells)
	s.fanOutput("p1", []byte(pasteOn), 0)
	if on, _ := pasteModeOf(s, "p1"); !on {
		t.Fatal("bracketed paste not tracked with track-cwd off")
	}
}

// A toggle the PTY hands over in two reads is seen at the seam, at every cut;
// and a later chunk that says nothing keeps the state.
func TestPasteModeSplitAcrossReads(t *testing.T) {
	full := []byte("x" + pasteOn + "y")
	for cut := 1; cut < len(full); cut++ {
		s := cwdServer(cwd.Off, cwd.Shells)
		s.fanOutput("p1", full[:cut], 0)
		s.fanOutput("p1", full[cut:], 0)
		s.fanOutput("p1", []byte("more output, no toggle"), 0)
		if on, _ := pasteModeOf(s, "p1"); !on {
			t.Fatalf("cut %d: split toggle missed", cut)
		}
	}
}

// A toggle in the seam is older than one wholly inside the new chunk.
func TestPasteModeChunkOutranksSeam(t *testing.T) {
	s := cwdServer(cwd.Off, cwd.Shells)
	s.fanOutput("p1", []byte("x\x1b[?20"), 0)
	s.fanOutput("p1", []byte("04h"+pasteOff), 0)
	if on, known := pasteModeOf(s, "p1"); on || !known {
		t.Fatalf("mode = on %v known %v, want off and known", on, known)
	}
}

func TestPasteModeForgottenOnExitAndClose(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	s.fanOutput("a1", []byte(pasteOn), 0)
	s.onPanelExit("a1", 0)
	if _, known := pasteModeOf(s, "a1"); known {
		t.Error("an exit should drop the paste mode")
	}

	s2, _, _ := gateServer(panel.Panel{ID: "b1", Kind: panel.Agent, State: panel.Running})
	s2.fanOutput("b1", []byte(pasteOn), 0)
	if err := s2.closePanel("b1"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, known := pasteModeOf(s2, "b1"); known {
		t.Error("a close should drop the paste mode")
	}
}

// A last read the PTY delivers after the close must not bring back the entries
// the close forgot: nothing would ever forget them again.
func TestPasteModeLateChunkAfterClose(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "b1", Kind: panel.Agent, State: panel.Running})
	s.fanOutput("b1", []byte(pasteOn), 0)
	if err := s.closePanel("b1"); err != nil {
		t.Fatalf("close: %v", err)
	}
	s.fanOutput("b1", []byte(pasteOn), 0)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pasteMode["b1"]; ok {
		t.Error("a late chunk re-created the paste mode of a closed panel")
	}
	if _, ok := s.pasteTail["b1"]; ok {
		t.Error("a late chunk re-created the paste tail of a closed panel")
	}
}

// After a re-run the mode is unknown until the new process sets it — even when
// the old one's output was still being read after its exit.
func TestPasteModeRerun(t *testing.T) {
	s, dir := identityServer(t)
	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", []string{"-c", "printf ran"}, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	waitFor(t, "the process to exit", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		idx := s.indexLocked(id)
		return idx >= 0 && s.panels[idx].State == panel.Exited
	})
	s.fanOutput(id, []byte(pasteOn), 0) // a late chunk of the old process's output

	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if _, known := pasteModeOf(s, id); known {
		t.Fatal("a re-run kept the old process's paste mode")
	}
}
