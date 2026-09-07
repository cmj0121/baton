package main

import (
	"os"
	"testing"
)

// The entry point is one line, and the line worth pinning is that the status
// reaches the process: a sweep that could not look must not exit 0 because the
// wrapper dropped the code on the floor.
func TestMainCarriesTheStatusOut(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BASE", "")

	got := -1
	exit = func(code int) { got = code }
	t.Cleanup(func() { exit = os.Exit })

	os.Args = []string{"stalecomment"}
	main()

	if got != 2 {
		t.Fatalf("exit %d, wanted 2 -- %s is not a git working tree", got, dir)
	}
}
