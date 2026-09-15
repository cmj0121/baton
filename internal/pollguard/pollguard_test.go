// Package pollguard holds one repo-wide check and no code: that a test which
// polls the daemon lets go of the CPU between round-trips.
//
// It exists because the bug it looks for has been diagnosed once already and
// found again afterwards. ab7a7dc (2026-09-07) reproduced CI's "red on three of
// the last six runs" as a specific shape — "a test competing for the scheduler
// with the goroutine it was waiting for" — and fixed six loops in one file.
// Two identical loops in two other files were never reached, and one of them
// went red on the v2.1.0 tag eight days later (#101).
//
// A comment explaining the fix does not stop the next copy of the loop. This
// does.
package pollguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roundTrip is a call that costs the daemon work to answer. A loop that makes
// one of these and never yields is the shape under test.
var roundTrip = map[string]bool{"List": true, "Do": true, "Send": true}

// yields is a call that hands the scheduler back to whoever the loop is waiting
// for. time.After covers the select-with-deadline form the fixed helpers use.
var yields = map[string]bool{"Sleep": true, "After": true, "Tick": true, "NewTicker": true, "NewTimer": true}

// repoRoot walks up from this package to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above this package")
	return ""
}

// selector reports the method name of a call like `c.List()` or `time.After(…)`.
func selector(n ast.Node) (recv, name string, ok bool) {
	call, isCall := n.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isID := sel.X.(*ast.Ident)
	if !isID {
		return "", "", false
	}
	return id.Name, sel.Sel.Name, true
}

// TestEveryPollingLoopYields is the guard. It walks every _test.go in the repo,
// finds the loops that make a round-trip, and fails the ones that never yield.
//
// It reads the SOURCE rather than measuring behaviour because there is nothing
// in a result to measure: a starved loop and a patient one return the same
// value, and the difference only shows on two cores under -race on someone
// else's machine.
func TestEveryPollingLoopYields(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not this test's job to report a parse error
		}
		ast.Inspect(file, func(n ast.Node) bool {
			var body *ast.BlockStmt
			switch loop := n.(type) {
			case *ast.ForStmt:
				// A counted loop — `for i := 0; i < n; i++` — is bounded by its
				// own arithmetic and makes n round-trips, not as many as it can.
				// Only `for {}` and `for cond {}` can spin.
				if loop.Init != nil && loop.Post != nil {
					return true
				}
				body = loop.Body
			case *ast.RangeStmt:
				return true // a range is bounded; it cannot spin
			default:
				return true
			}
			var polls, yielded bool
			ast.Inspect(body, func(inner ast.Node) bool {
				// A select with no default BLOCKS, which is the ticker idiom —
				// `nudge := time.NewTicker(…)` outside the loop and `<-nudge.C`
				// inside it — and blocking is yielding, whoever owns the channel.
				if sel, ok := inner.(*ast.SelectStmt); ok && !hasDefault(sel) {
					yielded = true
					return true
				}
				recv, name, ok := selector(inner)
				switch {
				case !ok:
				case roundTrip[name] && recv != "time":
					polls = true
				case yields[name] && recv == "time":
					yielded = true
				}
				return true
			})
			if polls && !yielded {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+fset.Position(n.Pos()).String()[len(path)+1:])
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d polling loop(s) re-ask as fast as the daemon can answer, starving the work they wait for "+
			"on two cores under -race (see ab7a7dc, #101):\n    %s",
			len(offenders), strings.Join(offenders, "\n    "))
	}
}

// hasDefault reports whether a select can fall through without blocking.
func hasDefault(sel *ast.SelectStmt) bool {
	for _, stmt := range sel.Body.List {
		if clause, ok := stmt.(*ast.CommClause); ok && clause.Comm == nil {
			return true
		}
	}
	return false
}
