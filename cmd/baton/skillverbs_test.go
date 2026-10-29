package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/cmj0121/baton/internal/skills"
)

// ctlSpan is one `baton ctl …` command as a skill writes it: everything after
// the prefix up to the end of its code span, its line, or a shell comment.
var ctlSpan = regexp.MustCompile("baton ctl ([^`\n#]*)")

// TestSkillsNameOnlyRealCtlVerbs holds every embedded skill to ctl's kong model.
// A skill is read as instruction, and the shell is the route every agent has —
// an outside agent with no MCP has nothing else — so a verb, subcommand or flag
// a skill names that ctl does not parse is a usage error the agent was told to
// run. The model is the parser itself, not a list beside it, so a verb renamed
// in ctl.go fails here instead of in an agent's terminal.
func TestSkillsNameOnlyRealCtlVerbs(t *testing.T) {
	parser, err := kong.New(&ctlCLI{}, kong.Name("baton ctl"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, set := range []skills.Set{skills.Worker, skills.Conductor, skills.Outside} {
		for _, n := range skills.Names(set) {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	seen := 0
	for _, name := range names {
		body, err := skills.Read(name)
		if err != nil {
			t.Fatalf("Read(%q): %v", name, err)
		}
		for _, m := range ctlSpan.FindAllStringSubmatch(string(body), -1) {
			seen++
			if problem := checkCtlSpan(parser.Model.Node, strings.Fields(m[1])); problem != "" {
				t.Errorf("skill %s names `baton ctl %s`: %s", name, strings.TrimSpace(m[1]), problem)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no `baton ctl` command found in any skill: the pattern no longer matches how skills write them")
	}
}

// checkCtlSpan walks one command's words down the kong tree: the first word
// must be a verb, a following bare word must be a subcommand wherever the verb
// has any, and every --flag must be one that command (or an ancestor) takes.
// Placeholders and arguments (<id>, quoted text, numbers) are not checked.
func checkCtlSpan(root *kong.Node, words []string) string {
	if len(words) == 0 {
		return "no verb"
	}
	node := root
	for i, w := range words {
		if strings.HasPrefix(w, "--") {
			flag, _, _ := strings.Cut(strings.TrimPrefix(w, "--"), "=")
			if !hasFlag(node, flag) {
				return "no flag --" + flag + " on " + node.Path()
			}
			continue
		}
		if len(node.Children) == 0 || !isWord(w) {
			continue
		}
		child := childNamed(node, w)
		if child == nil {
			if i == 0 {
				return "no verb " + w
			}
			return "no subcommand " + w + " under " + node.Path()
		}
		node = child
	}
	return ""
}

// isWord is a bare lowercase token, the shape of a verb; anything else is an
// argument or a placeholder.
func isWord(s string) bool { return verbWord.MatchString(s) }

var verbWord = regexp.MustCompile(`^[a-z][a-z-]*$`)

func childNamed(n *kong.Node, name string) *kong.Node {
	for _, c := range n.Children {
		if c.Name == name || slices.Contains(c.Aliases, name) {
			return c
		}
	}
	return nil
}

func hasFlag(n *kong.Node, name string) bool {
	for _, group := range n.AllFlags(false) {
		for _, f := range group {
			if f.Name == name || slices.Contains(f.Aliases, name) {
				return true
			}
		}
	}
	return false
}
