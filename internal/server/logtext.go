package server

import "github.com/cmj0121/baton/internal/scrub"

// The daemon's log is a file on the operator's disk, and some of what it quotes
// arrived off the wire. That combination is what this file exists for.
//
// Round one gave the log file its mode; what it left open was its VOLUME. Three
// INFO and WARN lines quote a peer's own string back verbatim — a dispatched
// brief a task.pre hook refused, and a fleet search's term, twice — and INFO is
// the default level, so an agent choosing what those strings say is an agent
// choosing how fast the disk fills and how often the 8 MiB rotation churns.
//
// The frame cap took the ceiling from unbounded to a megabyte a line, which is
// the difference between a fleet losing its disk and a fleet losing its log. It
// is not the end of the argument, because a log line is for READING: a megabyte
// of it is not evidence, and zerolog escapes a control byte to six characters, so
// a megabyte of ESC on the wire is six in the file.

// maxLogRunes caps a wire-supplied string quoted into a log field.
//
// It is this boundary's own number, in the way maxReasonRunes and maxNotifyRunes
// are theirs. What a log line owes its reader is enough of the text to RECOGNISE
// it — which brief, which search — beside the ids and reasons around it that are
// the actual subject. Two hundred runes is a full sentence of that, the same
// figure sanitizeReason settled on for the same question about a person's
// attention, and about four terminal lines when the operator greps for it.
//
// The value itself is never truncated by this — only the log's copy of it is. A
// brief is delivered whole, a search matches on the whole term; nothing downstream
// reads a log field back.
const maxLogRunes = 200

// logText renders a wire-supplied string for a log field: scrubbed of the control
// bytes zerolog would otherwise escape six-for-one, and cut to maxLogRunes.
func logText(s string) string { return scrub.Capped(s, maxLogRunes) }
