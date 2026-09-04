#!/usr/bin/env bash
#
# stale-comment-sweep-test.sh — prove the sweep can fail.
#
# A checker nobody has watched fail is a checker nobody knows works. The sweep
# makes two claims, and this asserts both against throwaway repositories built
# on the spot:
#
#   it catches a name that a rename left behind in a comment, and
#   it refuses to report success over a range it did not read.
#
# The second is the one worth a test. A sweep that exits 0 when it examined
# nothing looks exactly like a sweep that examined everything and approved it,
# and the difference is invisible until it matters.
#
# Usage: scripts/stale-comment-sweep-test.sh

set -uo pipefail

SWEEP="$(cd "$(dirname "$0")" && pwd)/stale-comment-sweep.sh"
FAILURES=0

# Repositories are built from scratch, so the ambient git config must not reach
# them: a global template, hook or init.defaultBranch would change what is
# committed and the assertions below would be about the developer's machine.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=sweep-test GIT_AUTHOR_EMAIL=sweep@test
export GIT_COMMITTER_NAME=sweep-test GIT_COMMITTER_EMAIL=sweep@test

# check <name> <expected-exit> <expected-substring-or-empty> -- <sweep args...>
check() {
	local name="$1" want_code="$2" want_text="$3"
	shift 4
	local out code
	out="$("${SWEEP}" "$@" 2>&1)"
	code=$?

	if [ "${code}" -ne "${want_code}" ]; then
		echo "  FAIL  ${name}: exit ${code}, wanted ${want_code}"
		awk '{ print "          " $0 }' <<<"${out}"
		FAILURES=$((FAILURES + 1))
		return
	fi
	if [ -n "${want_text}" ] && ! grep -q -- "${want_text}" <<<"${out}"; then
		echo "  FAIL  ${name}: exit ${code} as wanted, but output never says '${want_text}'"
		awk '{ print "          " $0 }' <<<"${out}"
		FAILURES=$((FAILURES + 1))
		return
	fi
	echo "  ok    ${name} (exit ${code})"
}

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

# ---------------------------------------------------------------------------
# A repository whose second commit renames a function and updates one of the
# two comments naming it. The straggler is the whole point.
# ---------------------------------------------------------------------------
REPO="${TMP}/rename"
mkdir -p "${REPO}"
cd "${REPO}" || exit 1
git init -q -b main .

cat >lock.go <<'GO'
package lock

// explainLocked reports why the lock is held.
func explainLocked() string {
	return "held"
}
GO
git add -A && git commit -q -m "baseline"

cat >lock.go <<'GO'
package lock

// describeHold reports why the lock is held.
func describeHold() string {
	return "held"
}

// Callers must not take the mutex before explainLocked runs, or the
// two paths deadlock.
func caller() string {
	return describeHold()
}
GO
git add -A && git commit -q -m "rename the function, miss the second comment"

echo "catching a stale name:"
check "the straggler is reported" 1 "explainLocked" -- HEAD~1

# The same tree with the comment corrected must go quiet, or the tool reports
# every branch and teaches people to ignore it.
sed -i.bak 's/explainLocked runs/describeHold runs/' lock.go && rm -f lock.go.bak
git add -A && git commit -q -m "fix the straggler"
check "the corrected comment is quiet" 0 "" -- HEAD~1

# Prose that merely looks like code must not be reported. A capitalised word
# opening a sentence is the commonest shape in any comment in any repository.
cat >>lock.go <<'GO'

// Callers hold the lock. The Deadline is advisory and TODO items are tracked
// elsewhere. IDs and URLs are formatted by the caller.
func note() {}
GO
git add -A && git commit -q -m "add ordinary English prose"
check "ordinary prose is not an identifier" 0 "" -- HEAD~1

# ---------------------------------------------------------------------------
# The refusals. Each of these once had an obvious wrong answer -- exit 0 --
# and exit 0 is indistinguishable from having read the whole range and
# approved it.
# ---------------------------------------------------------------------------
echo ""
echo "refusing to vouch for what it did not read:"
check "an empty range is not a pass" 2 "NOTHING CHECKED" -- HEAD
check "an unresolvable base is not a pass" 2 "NOTHING CHECKED" -- no/such/ref

# A commit that adds Go code but no comments is a truthful nothing-to-do, and
# the sweep may only say so because an independent count of the raw diff
# agrees. That cross-check is what separates it from the empty range above.
cat >>lock.go <<'GO'

func plain() int { return 1 }
GO
git add -A && git commit -q -m "add code carrying no comment"
check "a commit with no added comments passes honestly" 0 "nothing here to sweep" -- HEAD~1

# ---------------------------------------------------------------------------
# A repository of nothing but comments. If the lexer ever routes code into the
# comment stream this is what it looks like from the inside, and the sweep has
# to notice rather than report the whole file as stale.
# ---------------------------------------------------------------------------
echo ""
echo "noticing its own lexer failing:"
REPO2="${TMP}/nocode"
mkdir -p "${REPO2}"
cd "${REPO2}" || exit 1
git init -q -b main .
echo "package a" >a.go
git add -A && git commit -q -m "baseline"

# `package` and `func` are not optional in Go, so a code index holding neither
# cannot have been built from code, and the sweep must say so rather than
# report the tree.
printf 'package a\n\nfunc b() {}\n' >a.go
git add -A && git commit -q -m "real code, so the index is real"
check "a readable tree is read" 0 "" -- HEAD~1

# A tree whose .go files carry no code at all is what a broken lexer looks
# like from the inside, and it is reachable without breaking anything: a file
# holding only comments produces an empty code stream either way.
#
# The failure this guards against was real and was subtle. With nothing in the
# stream the name filter matched nothing, exited 1 as grep does, and under
# `pipefail` took the script with it -- exiting 1, the status this sweep spends
# on "stale names found". A fault in the tool arrived looking like a finding
# about the code, which is the one way a checker can be worse than absent.
REPO3="${TMP}/comments-only"
mkdir -p "${REPO3}"
cd "${REPO3}" || exit 1
git init -q -b main .
printf '// a file of pure comment\n' >a.go
git add -A && git commit -q -m "baseline"
printf '// a file of pure comment\n// naming mostlyProse, which exists nowhere\n' >a.go
git add -A && git commit -q -m "a comment naming something absent"
check "a tool fault is not dressed up as a finding" 2 "comment/code split failed" -- HEAD~1

echo ""
if [ "${FAILURES}" -gt 0 ]; then
	echo "!! ${FAILURES} assertion(s) failed."
	exit 1
fi
echo ">> the sweep catches a straggler, ignores prose, and refuses an empty range."
