package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
)

// TestApplyPrefsConsumesEveryPref is the guard that retires a whole class of bug
// rather than the two instances of it that were found.
//
// prefs is a plain struct and applyPrefs is a flat list of assignments, so adding
// a setting is three edits in three files — config.Settings, prefsFromConfig, and
// applyPrefs — and forgetting the third one produces NO symptom a compiler or a
// reviewer can see. The field is read from the file, mapped onto prefs, and then
// dropped on the floor; the setting simply never does anything, the default stands
// forever, and the only way to notice is for somebody to configure it and wonder
// why nothing changed. That is exactly how inbox-done and inbox-snooze shipped.
//
// It is a source-level check because a behavioural one cannot be written: prefs'
// fields are unexported, so reflection cannot set them, and a hand-written
// field-by-field assertion is the same list that was got wrong in the first place —
// it would need updating by the same person who forgot to update applyPrefs. The
// AST, by contrast, cannot be forgotten: it reads whatever the struct actually
// says today.
func TestApplyPrefsConsumesEveryPref(t *testing.T) {
	fset := token.NewFileSet()

	keys, err := parser.ParseFile(fset, "keys.go", nil, 0)
	if err != nil {
		t.Fatalf("parse keys.go: %v", err)
	}
	fields := prefsFieldNames(keys)
	if len(fields) == 0 {
		t.Fatal("found no fields on the prefs struct — has it moved out of keys.go?")
	}

	main, err := parser.ParseFile(fset, "tui.go", nil, 0)
	if err != nil {
		t.Fatalf("parse tui.go: %v", err)
	}
	used := prefsFieldsRead(t, main)

	for _, name := range fields {
		if !used[name] {
			t.Errorf("prefs.%s is loaded from the config but never reaches the model: "+
				"applyPrefs does not read p.%s", name, name)
		}
	}
}

// prefsFieldNames lists the field names of the prefs struct as the source declares
// them.
func prefsFieldNames(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "prefs" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, nm := range fld.Names {
				out = append(out, nm.Name)
			}
		}
		return false
	})
	return out
}

// prefsFieldsRead is every field applyPrefs reads off its prefs argument, found by
// the parameter's own name rather than by assuming it is called "p".
func prefsFieldsRead(t *testing.T, f *ast.File) map[string]bool {
	t.Helper()
	used := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "applyPrefs" || fn.Body == nil {
			continue
		}
		param := ""
		for _, p := range fn.Type.Params.List {
			if id, ok := p.Type.(*ast.Ident); ok && id.Name == "prefs" && len(p.Names) == 1 {
				param = p.Names[0].Name
			}
		}
		if param == "" {
			t.Fatal("applyPrefs does not take a prefs parameter by name")
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == param {
				used[sel.Sel.Name] = true
			}
			return true
		})
		return used
	}
	t.Fatal("no applyPrefs found in tui.go")
	return nil
}

// TestInboxPrefsReachTheCockpit is the behavioural half: the two settings that
// were being dropped now land on the model, with the defaults the docs promise.
//
// inbox-done defaulting to on is what makes the queue clearable at all — with it
// off the inbox holds only questions, failures and wedges — and it is also what a
// group header's ◆N counts, so a cockpit stuck on false was under-reporting the
// dashboard as well as the queue.
func TestInboxPrefsReachTheCockpit(t *testing.T) {
	m := baseModel().applyPrefs(prefsFromConfig(config.Config{}))
	if !m.inboxDone {
		t.Error("inbox-done should default to on in the cockpit")
	}
	if m.inboxSnooze != defaultInboxSnooze {
		t.Errorf("inbox-snooze = %v, want the %v default", m.inboxSnooze, defaultInboxSnooze)
	}

	off := false
	m = baseModel().applyPrefs(prefsFromConfig(config.Config{
		Settings: config.Settings{InboxDone: &off, InboxSnooze: "45s"},
	}))
	if m.inboxDone {
		t.Error("inbox-done: false should reach the cockpit")
	}
	if m.inboxSnooze != 45*time.Second {
		t.Errorf("inbox-snooze = %v, want 45s", m.inboxSnooze)
	}
	if m.effSnooze() != 45*time.Second {
		t.Errorf("effSnooze = %v, want the configured 45s", m.effSnooze())
	}
}

// TestInboxDoneReachesTheNeedCount closes the loop the bug actually broke: with
// inbox-done stuck at false a finished agent was in neither the queue nor its
// group's ◆N, so the dashboard quietly disagreed with its own documentation.
func TestInboxDoneReachesTheNeedCount(t *testing.T) {
	m := baseModel().applyPrefs(prefsFromConfig(config.Config{}))
	m.inboxWire = needFleet()
	m.fleet = mergeFleet(m.inboxWire)
	assertNeedMatchesInbox(t, m)

	for _, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == "api" && it.need != 4 {
			t.Fatalf("api need = %d with the default settings, want 4 (done included)", it.need)
		}
	}
}
