// Package panellog writes a panel's terminal output to a plain-text file on the
// machine the fleet runs on.
//
// The point is the artifact. baton's unit of work is an agent, so what a panel
// produces over a long run is a TRANSCRIPT — something to grep, to paste into an
// issue, to hand to another agent, to keep for an audit — and the in-memory
// replay ring cannot be any of those: it is bounded, and it dies with the panel.
//
// What lands on disk is plain text with the escape sequences stripped (see
// Stripper). The cost of that is honest and documented rather than hidden: an
// agent that redraws in place leaves its intermediate states in the file as
// repeated lines. A raw-bytes mode may follow; this is not it.
//
// The package owns the file and nothing else. It does not know what a panel is,
// when one exits, or who asked for the log — the daemon holds all of that and
// drives a Sink through Open / Suspend / Resume / Close.
package panellog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultMaxMB is the size a log rolls at when the config names none. A runaway
// build can produce gigabytes in minutes, and baton already argues in
// docs/LIMITS.md that nothing should be able to take the machine with it — a log
// is not an exception.
const DefaultMaxMB = 64

// mib is one mebibyte, the unit log-max-mb is written in.
const mib = 1 << 20

// filePerm is what a log is created with, and it is 0600 on purpose: a shell
// panel's log holds everything typed into that shell. dirPerm is the same
// reasoning one level up.
const (
	filePerm os.FileMode = 0o600
	dirPerm  os.FileMode = 0o700
)

// maxSlug caps one path component built from a panel title, so a panel someone
// named with a paragraph still yields a filename the OS accepts.
const maxSlug = 48

// Sink is one panel's open log file. It survives the process it is logging: a
// panel that exits suspends its sink (the file is closed and flushed) and a
// respawn resumes it, appending under a new session marker rather than
// truncating — the previous run is usually why you are reading the file.
//
// Safe for concurrent use.
type Sink struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	n        int64 // bytes in the current generation, for the size roll
	strip    Stripper
}

// MaxBytes is the size a log rolls at, from a config value in mebibytes. A
// non-positive value means "unset" and takes the default.
func MaxBytes(maxMB int) int64 {
	if maxMB <= 0 {
		maxMB = DefaultMaxMB
	}
	return int64(maxMB) * mib
}

// Slug reduces a string to the characters a filename should carry: runs of
// anything else collapse to a single dash, and the result is trimmed and capped.
// An input with nothing usable in it yields "panel", so a name is never empty.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= maxSlug {
			break
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "panel"
	}
	return out
}

// FileName is the log's name for a panel: <yyyy-mm-dd>-<title>-<id>.log, each
// part slugified. The date leads so a directory sorts chronologically; the id
// trails so two panels with the same title on the same day never collide.
func FileName(title, id string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%s.log", now.Format("2006-01-02"), Slug(title), Slug(id))
}

// Open creates dir if needed and opens the log for a panel in APPEND mode,
// returning the sink ready to be written. maxBytes is the size the file rolls
// at; a non-positive value takes the default.
//
// Appending rather than truncating is the whole shape of this file: a panel
// logged, stopped, and logged again keeps both runs, and so does a respawn.
func Open(dir, title, id string, maxBytes int64, now time.Time) (*Sink, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("no log directory configured")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	if maxBytes <= 0 {
		maxBytes = MaxBytes(0)
	}
	s := &Sink{path: filepath.Join(dir, FileName(title, id, now)), maxBytes: maxBytes}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the file this sink writes to. It is stable for the sink's whole life,
// including across a suspend and resume, so the cockpit's "open my log" can name
// it without asking twice.
func (s *Sink) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Start writes the opening ceremony: a banner naming the panel, then the replay
// prefix, then the marker that live output follows.
//
// The replay flush is the point of starting this way. You reach for logging
// BECAUSE something interesting just happened, so a log that began at the
// keypress would miss the thing that made you press it. What it cannot honestly
// claim is when any of it happened — the ring is bytes, not events — so the block
// says so rather than pretending otherwise.
func (s *Sink) Start(title, dir string, replay []byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := fmt.Sprintf("=== baton log · %s ===\n", strings.Trim(strings.TrimSpace(title)+" · "+strings.TrimSpace(dir), " ·"))
	if err := s.raw([]byte(head)); err != nil {
		return err
	}
	if err := s.markLocked("logging started", now); err != nil {
		return err
	}
	if len(replay) == 0 {
		return nil
	}
	var one Stripper
	text := append(one.Strip(replay), one.Flush()...)
	if len(strings.TrimSpace(string(text))) == 0 {
		return nil
	}
	if err := s.raw([]byte("--- replay buffer: output from before logging started; its timestamps are not known ---\n")); err != nil {
		return err
	}
	if err := s.raw(withTrailingNewline(text)); err != nil {
		return err
	}
	return s.markLocked("live output follows", now)
}

// Write appends chunk to the log with its escape sequences stripped, rolling the
// file first if it has reached its size cap. A suspended sink (the panel's
// process has exited) silently discards, so the daemon's output path needs no
// second check.
func (s *Sink) Write(chunk []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	text := s.strip.Strip(chunk)
	if len(text) == 0 {
		return nil
	}
	if s.n >= s.maxBytes {
		if err := s.rollLocked(); err != nil {
			return err
		}
	}
	return s.raw(text)
}

// Suspend closes the file with a marker saying why, keeping the sink (and its
// path) so a respawn can Resume into the same log. It is what a panel's process
// exiting does.
func (s *Sink) Suspend(reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked(reason, now)
}

// Resume reopens the log and marks a new session in it — what a respawn does.
// Resuming an already-open sink is a no-op, so a double respawn cannot double the
// marker.
func (s *Sink) Resume(reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		return nil
	}
	if err := s.open(); err != nil {
		return err
	}
	return s.markLocked(reason, now)
}

// Close writes a final marker and closes the file for good. It is what switching
// logging off, closing the panel, and shutting the daemon down all do. Closing an
// already-closed sink is a no-op.
func (s *Sink) Close(reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked(reason, now)
}

// closeLocked flushes the stripper's held bytes, writes the marker and closes.
// Caller holds s.mu.
func (s *Sink) closeLocked(reason string, now time.Time) error {
	if s.f == nil {
		return nil
	}
	if rest := s.strip.Flush(); len(rest) > 0 {
		_ = s.raw(rest) // a partial sequence at the end is text, not nothing
	}
	err := s.markLocked(reason, now)
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	s.f = nil
	return err
}

// open opens (or reopens) the log in append mode and reads back its size, so the
// roll counts what the file holds rather than what this process wrote to it.
// Caller holds s.mu, or holds the sink exclusively (Open).
func (s *Sink) open() error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return fmt.Errorf("open log %s: %w", s.path, err)
	}
	s.f = f
	s.n = 0
	if fi, serr := f.Stat(); serr == nil {
		s.n = fi.Size()
	}
	return nil
}

// rollLocked moves the log aside to "<path>.1" and starts a new one, keeping the
// two most recent generations and no more. Caller holds s.mu and has a live file.
func (s *Sink) rollLocked() error {
	prev := s.path + ".1"
	if err := s.f.Close(); err != nil {
		s.f = nil
		return fmt.Errorf("close log before roll %s: %w", s.path, err)
	}
	s.f = nil
	if err := os.Remove(prev); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("drop the previous log generation %s: %w", prev, err)
	}
	if err := os.Rename(s.path, prev); err != nil {
		return fmt.Errorf("roll log %s: %w", s.path, err)
	}
	if err := s.open(); err != nil {
		return err
	}
	return s.raw([]byte(fmt.Sprintf("=== continued from %s ===\n", filepath.Base(prev))))
}

// markLocked writes one "=== reason · timestamp ===" line. Caller holds s.mu.
func (s *Sink) markLocked(reason string, now time.Time) error {
	return s.raw([]byte(fmt.Sprintf("=== %s · %s ===\n", reason, now.Format(time.RFC3339))))
}

// raw writes bytes through untouched and counts them toward the roll. Caller
// holds s.mu; a closed sink discards.
func (s *Sink) raw(p []byte) error {
	if s.f == nil {
		return nil
	}
	n, err := s.f.Write(p)
	s.n += int64(n)
	if err != nil {
		return fmt.Errorf("write log %s: %w", s.path, err)
	}
	return nil
}

// withTrailingNewline makes sure a block ends on its own line, so the marker
// after it does not land mid-line.
func withTrailingNewline(p []byte) []byte {
	if len(p) > 0 && p[len(p)-1] == '\n' {
		return p
	}
	return append(append([]byte(nil), p...), '\n')
}
