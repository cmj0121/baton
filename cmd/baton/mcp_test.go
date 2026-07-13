package main

import (
	"os"
	"testing"
)

// TestMcpMainWriteError covers mcpMain's failure branch (exit code 1): stdin
// carries one framed message that produces a reply, but stdout's read end is
// closed, so the server's reply write fails and Serve returns an error. Using a
// fresh pipe (not fd 1) means the failed write yields EPIPE rather than the
// SIGPIPE crash the runtime reserves for the real stdout.
func TestMcpMainWriteError(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	go func() {
		_, _ = inW.Write([]byte("{malformed json}\n")) // a frame that draws a parse-error reply
		_ = inW.Close()
	}()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	_ = outR.Close() // the reply write now fails with EPIPE

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = inR.Close()
		_ = outW.Close()
	}()

	if code := mcpMain(); code != 1 {
		t.Fatalf("mcpMain with a broken stdout = %d, want 1", code)
	}
}
