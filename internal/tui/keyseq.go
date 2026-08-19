package tui

import (
	"slices"
	"strings"
)

// A binding's key is a SEQUENCE: one or more tokens separated by spaces, so
// "p" is one key and "g c" is two pressed in order. The first token of a
// multi-token binding is a LANDING — a key that does nothing on its own and
// opens a family (n spawns, v draws, g groups, x confirms itself by being
// pressed twice). Landings are what keep the everyday verbs on single keys
// while the long tail stays reachable and, more importantly, discoverable:
// press one and the status bar names what it can take next.
//
// Matching walks the whole key map rather than a prepared trie. The map is
// tens of bindings and this runs once per keystroke, so the linear scan is
// free, and it keeps "what does this sequence mean" answerable by reading one
// function instead of a data structure built somewhere else.

// tok canonicalises a bubbletea key name into a sequence token. Only the space
// bar needs it: bubbletea calls it " ", which cannot survive a space-separated
// sequence, so it is written and matched as "space".
func tok(key string) string {
	if key == " " {
		return "space"
	}
	return key
}

// normSeq canonicalises a configured key into a space-separated sequence: runs
// of whitespace collapse, and a key that is nothing but a space (how an older
// config spells the space bar) becomes the "space" token.
func normSeq(key string) string {
	if strings.TrimSpace(key) == "" {
		if key == "" {
			return ""
		}
		return "space"
	}
	return strings.Join(strings.Fields(key), " ")
}

// seq splits a binding's key into its tokens.
func (b binding) seq() []string { return strings.Fields(normSeq(b.key)) }

// seqLabel renders a sequence for a legend: each token through keyLabel, joined
// by spaces — "ctrl+t" becomes "C-t", "space" stays "space", "g c" stays "g c".
func seqLabel(key string) string {
	return strings.Join(labelTokens(strings.Fields(normSeq(key))), " ")
}

// seqMatch is what a run of typed tokens amounts to against the key map.
type seqMatch int

const (
	// seqNone: no binding starts with these tokens. The run is dead; say so and
	// drop it rather than holding keys the user cannot complete.
	seqNone seqMatch = iota

	// seqPartial: a landing. No binding ends here but at least one continues,
	// so hold the run and wait for the next key (or for the timeout).
	seqPartial

	// seqExact: a binding ends here and nothing extends it. Fire immediately —
	// this is every binding in the default key map, which is why the timeout
	// never delays a keystroke unless the user binds an ambiguity themselves.
	seqExact

	// seqExactPartial: a binding ends here AND another extends it, the way "d"
	// and "dd" relate in vim. Neither answer is safe yet, so hold the run and
	// let the timeout fire the binding that ends here.
	seqExactPartial
)

// matchSeq resolves a run of typed tokens against the bindings want accepts.
// The returned binding is meaningful for seqExact and seqExactPartial only.
func matchSeq(binds []binding, tokens []string, want func(binding) bool) (binding, seqMatch) {
	if len(tokens) == 0 {
		return binding{}, seqNone // nothing typed yet resolves to nothing, never to "every landing"
	}
	var hit binding
	exact, partial := false, false
	for _, b := range binds {
		if want != nil && !want(b) {
			continue
		}
		s := b.seq()
		switch {
		case len(s) == 0:
			continue
		case len(s) == len(tokens) && hasSeqPrefix(s, tokens):
			// First in key-map order wins. Two bindings CAN share a sequence —
			// nothing stops a rebind from landing on a key another binding
			// already holds — and when they do, the one listed first is the one
			// the key list showed you.
			if !exact {
				hit, exact = b, true
			}
		case len(s) > len(tokens) && hasSeqPrefix(s, tokens):
			partial = true
		}
	}
	switch {
	case exact && partial:
		return hit, seqExactPartial
	case exact:
		return hit, seqExact
	case partial:
		return binding{}, seqPartial
	}
	return binding{}, seqNone
}

// hasSeqPrefix reports whether the run s starts with p. An empty p is a prefix
// of everything, which is what lets a hint with nothing typed list every first
// token; matchSeq guards the empty case itself rather than relying on this.
func hasSeqPrefix(s, p []string) bool {
	return len(p) <= len(s) && slices.Equal(s[:len(p)], p)
}

// contin is one key a landing can take next, for the status bar's hint. b is the
// binding it completes, or the zero binding when the token is itself another
// landing — which the hint renders as an ellipsis rather than pretending it is
// an action.
type contin struct {
	key string
	b   binding
}

// seqNext lists what the tokens typed so far can take next, in key-map order so
// the hint reads in the same order as the key list. A token that both completes
// a binding and continues into a longer one is reported as the binding it
// completes; the longer one is reachable but is not what the hint advertises.
func seqNext(binds []binding, tokens []string, want func(binding) bool) []contin {
	var out []contin
	seen := map[string]int{}
	for _, b := range binds {
		if want != nil && !want(b) {
			continue
		}
		s := b.seq()
		if len(s) <= len(tokens) || !hasSeqPrefix(s, tokens) {
			continue
		}
		next := s[len(tokens)]
		c := contin{key: next}
		if len(s) == len(tokens)+1 { // this key completes b
			c.b = b
		}
		if i, ok := seen[next]; ok {
			if out[i].b.name == "" { // a landing already recorded, now completed by b
				out[i] = c
			}
			continue
		}
		seen[next] = len(out)
		out = append(out, c)
	}
	return out
}

// ambiguous lists the bindings whose sequence is the strict start of another's,
// which is the only way to make a keystroke wait on the timeout. The key map
// editor reports these back to the user; nothing refuses them, since a config
// that wants vim's d/dd relationship is entitled to it.
func ambiguous(binds []binding, want func(binding) bool) [][2]binding {
	var out [][2]binding
	for _, a := range binds {
		if want != nil && !want(a) {
			continue
		}
		as := a.seq()
		if len(as) == 0 {
			continue
		}
		for _, b := range binds {
			if want != nil && !want(b) {
				continue
			}
			bs := b.seq()
			if len(bs) > len(as) && hasSeqPrefix(bs, as) {
				out = append(out, [2]binding{a, b})
				break
			}
		}
	}
	return out
}
