package tui

import (
	"io"
	"sync"
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

// drainEmu spins up a zoomReader-style drain of the emulator's input pipe and returns
// the collected bytes plus a stop func that closes the pipe and waits for the drain.
// The emulator answers terminal queries onto this pipe, so whatever ends up here is
// exactly what zoomReader would forward to the program as panel.input.
func drainEmu(emu *vt.SafeEmulator) (get func() []byte, stop func()) {
	var mu sync.Mutex
	var got []byte
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := emu.Read(buf)
			if n > 0 {
				mu.Lock()
				got = append(got, buf[:n]...)
				mu.Unlock()
			}
			if err != nil {
				close(done)
				return
			}
		}
	}()
	get = func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), got...)
	}
	stop = func() {
		if pw, ok := emu.InputPipe().(*io.PipeWriter); ok {
			_ = pw.Close()
		}
		<-done
	}
	return get, stop
}

// TestWriteEmuStripsQueryReplies is the regression guard for the input-line leak: live
// output carrying a program's terminal probes (a DA query, the in-band-resize enable)
// must not make the emulator answer, because the answer is forwarded straight back to
// the program's input. writeEmu strips the probes first, so nothing is forwarded.
func TestWriteEmuStripsQueryReplies(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 24)
	get, stop := drainEmu(emu)

	// A /clear re-init burst: alt-screen re-enter + clear draw; the DA and in-band-resize
	// probes would each be answered onto the input pipe if not stripped.
	writeEmu(emu, []byte("\x1b[?1049h\x1b[2J\x1b[c\x1b[?2048hready$ "))

	stop()
	if leaked := get(); len(leaked) != 0 {
		t.Errorf("writeEmu forwarded %q as input; want nothing", leaked)
	}
}

// TestWriteEmuPositiveControl proves the drain harness actually observes a reply when
// one is produced: feeding the same in-band-resize enable straight to emu.Write (the
// unstripped path) DOES inject a size report, so the negative test above is meaningful.
func TestWriteEmuPositiveControl(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 24)
	get, stop := drainEmu(emu)

	if _, err := emu.Write([]byte("\x1b[?2048h")); err != nil {
		t.Fatalf("emu.Write: %v", err)
	}

	stop()
	if reply := get(); len(reply) == 0 {
		t.Fatal("harness saw no reply for an unstripped in-band-resize enable; the negative test would be vacuous")
	}
}
