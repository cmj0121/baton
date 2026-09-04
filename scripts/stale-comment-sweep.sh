#!/usr/bin/env bash
#
# stale-comment-sweep.sh — find identifiers that live only in comments.
#
# A rename or a deletion leaves the old name sitting in the comment beside it.
# The compiler never sees a comment, so nothing catches that but a reader. This
# sweep does the mechanical half: it walks every comment line the range *adds*
# to a .go file, pulls out every code-shaped name, and asks whether that name
# still appears anywhere in the tree's *code* once comments are stripped out.
# A name that survives only in prose is the residue of an edit that moved on
# without it.
#
# It needs no list of what was renamed. That is the point: it does not care
# whether the rename was deliberate, and it has no way to be told the wrong
# answer by an out-of-date list.
#
# What it does NOT catch, so that nobody reads a green run as more than it is:
# a comment that enumerates cases and goes stale when a case is added, and a
# comment that states an arithmetic result and goes stale when the function
# changes. Neither involves an identifier, so no grep can see either. The only
# form of those claims that survives the next edit is an assertion in a test.
#
# Usage: scripts/stale-comment-sweep.sh [base-revision]
#        BASE=origin/main scripts/stale-comment-sweep.sh
#
# Exit status:
#   0  comment lines were examined and every name in them exists in code
#   1  a name was found that exists in comments but not in code
#   2  NOTHING WAS CHECKED -- see below

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# ---------------------------------------------------------------------------
# Exit 2 is the whole reason this script is worth having.
#
# The version of this sweep that was thrown away exited 0 on an empty range.
# That is worse than not running it: a check that cannot distinguish "I looked
# and found nothing" from "I never looked" reports a green light for work it
# never read. Every path below that cannot honestly claim to have examined
# something exits 2 instead of 0, and every claim it makes about having
# examined something is cross-checked against an independent count.
# ---------------------------------------------------------------------------
die_unchecked() {
	echo ""
	echo "!! NOTHING CHECKED: $1"
	echo "   This is a failure, not a pass. The sweep did not examine any"
	echo "   comment line, so it cannot vouch for this range."
	exit 2
}

# ---------------------------------------------------------------------------
# Resolve the range.
# ---------------------------------------------------------------------------
BASE="${1:-${BASE:-}}"

if [ -z "${BASE}" ]; then
	# Default to everything this branch adds. Take the NEAREST merge-base among
	# the candidates rather than the first that resolves: a remote-tracking ref
	# can be stale, or belong to a mirror nobody pushes to, and taking it on
	# faith sweeps every commit since that mirror last moved. In this repo
	# `origin` is an unreachable gitea and origin/main sat 130 commits behind
	# `main`, so first-match swept a whole release's worth of history and
	# reported eight names from work that had nothing to do with the branch.
	#
	# Nearest is the right rule in both directions: a stale local `main` loses
	# to a fresher remote just as a stale remote loses to a fresher local.
	for candidate in origin/main main GITHUB/main upstream/main; do
		git rev-parse --verify --quiet "${candidate}^{commit}" >/dev/null || continue
		mb="$(git merge-base "${candidate}" HEAD 2>/dev/null)" || continue
		[ -n "${mb}" ] || continue
		if [ -z "${BASE}" ] || git merge-base --is-ancestor "${BASE}" "${mb}"; then
			BASE="${mb}"
		fi
	done
	if [ -z "${BASE}" ] || [ "$(git rev-parse "${BASE}")" = "$(git rev-parse HEAD)" ]; then
		BASE="$(git rev-parse --verify --quiet 'HEAD~1' || true)"
	fi
fi

[ -n "${BASE}" ] || die_unchecked "no base revision could be resolved (is this a shallow or single-commit clone?)"
git rev-parse --verify --quiet "${BASE}^{commit}" >/dev/null ||
	die_unchecked "base revision '${BASE}' does not resolve to a commit"

BASE_SHA="$(git rev-parse "${BASE}")"
HEAD_SHA="$(git rev-parse HEAD)"

[ "${BASE_SHA}" != "${HEAD_SHA}" ] ||
	die_unchecked "the range ${BASE}..HEAD is empty (base and HEAD are the same commit)"

echo ">> sweeping comments added by ${BASE_SHA:0:12}..${HEAD_SHA:0:12}"

CHANGED="$(git diff --name-only --diff-filter=d "${BASE_SHA}" -- '*.go' || true)"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# ---------------------------------------------------------------------------
# The Go lexer, such as it is.
#
# Splits each line into the part the compiler reads and the part it discards,
# which is the one distinction this whole script rests on. A regex cannot make
# it: `//` inside a string literal is not a comment, and a `"` inside a comment
# does not open a string. So this walks characters and carries two pieces of
# state across lines -- inside a /* */ block, and inside a `` raw string ``,
# the only two Go constructs that span one.
#
# Emits one "<line>\tC\t<code>" and one "<line>\tM\t<comment>" per input line.
# String literals stay in the code stream on purpose: a name that appears only
# in a string still exists in the file, and calling it stale would be a lie.
# ---------------------------------------------------------------------------
cat >"${WORK}/split.awk" <<'AWK'
BEGIN { st = 0 }   # 0 = code, 1 = block comment, 2 = raw string
{
	n = length($0); code = ""; com = ""; i = 1
	while (i <= n) {
		c = substr($0, i, 1)
		if (st == 1) {
			if (c == "*" && substr($0, i+1, 1) == "/") { st = 0; i += 2 }
			else { com = com c; i++ }
			continue
		}
		if (st == 2) {
			code = code c
			if (c == "`") st = 0
			i++
			continue
		}
		if (c == "/" && substr($0, i+1, 1) == "/") { com = com substr($0, i+2); break }
		if (c == "/" && substr($0, i+1, 1) == "*") { st = 1; i += 2; continue }
		if (c == "`") { st = 2; code = code c; i++; continue }
		if (c == "\"" || c == "'") {
			q = c; code = code c; i++
			while (i <= n) {
				d = substr($0, i, 1); code = code d; i++
				if (d == "\\") { if (i <= n) { code = code substr($0, i, 1); i++ }; continue }
				if (d == q) break
			}
			continue
		}
		code = code c; i++
	}
	# Tab separates the three fields, so no tab may survive inside one. Go is
	# tab-indented, which put every indented line's code into a field the
	# reader was not looking at until this line existed.
	gsub(/\t/, " ", code); gsub(/\t/, " ", com)
	printf "%d\tC\t%s\n", FNR, code
	printf "%d\tM\t%s\n", FNR, com
}
AWK

# ---------------------------------------------------------------------------
# What counts as a code-shaped name.
#
# The cost of getting this wrong is not a missed rename, it is a hook that
# people turn off. English prose is full of tokens that a naive identifier
# regex accepts -- every capitalised word that opens a sentence, every acronym.
# So a bare word is never code-shaped, however it is capitalised. A name
# qualifies only on evidence no English word carries: an underscore, or a case
# change past the first letter.
#
#   explainLocked  yes -- lower-to-upper inside the word
#   parseURL       yes
#   max_retries    yes -- underscore
#   Panel, The     no  -- a capitalised word is a sentence opening
#   TODO, API, ID  no  -- an acronym has no case change
#   NaNs, IDs, PRs no  -- acronym plurals; English, and the one false positive
#                         the throwaway version of this sweep reported
# ---------------------------------------------------------------------------
cat >"${WORK}/shaped.awk" <<'AWK'
function shaped(t) {
	if (t !~ /^[A-Za-z_][A-Za-z0-9_]*$/) return 0
	if (t ~ /^[A-Z]+s$/) return 0             # acronym plural: IDs, NaNs, URLs
	if (t ~ /_/ && t ~ /[A-Za-z0-9]/) return 1
	if (t ~ /[A-Z]/ && t ~ /[a-z]/ && t !~ /^[A-Z][a-z]*$/) return 1
	return 0
}
{
	line = $0
	gsub(/[^A-Za-z0-9_]/, " ", line)
	m = split(line, tok, " ")
	for (j = 1; j <= m; j++)
		if (shaped(tok[j])) print tok[j]
}
AWK

# ---------------------------------------------------------------------------
# Build the index of every name the code actually contains.
# ---------------------------------------------------------------------------
# The braces around grep matter under `set -o pipefail`: a grep that matches
# nothing exits 1 and takes the pipeline, and so the script, down with it --
# with status 1, which this script has already spent on "stale names found".
# An empty index has to reach the integrity check below and be reported as the
# breakage it is, not as a finding about the code.
git ls-files -z '*.go' | xargs -0 awk -f "${WORK}/split.awk" |
	awk -F'\t' '$2 == "C" { print $3 }' |
	tr -c 'A-Za-z0-9_' '\n' |
	{ grep -E '^[A-Za-z_][A-Za-z0-9_]*$' || true; } |
	sort -u >"${WORK}/code-names"

# Non-Go tracked sources hold names too -- proto and yaml declare fields that
# Go comments legitimately mention. Comments there are not stripped, which
# costs recall and buys no false positives, and recall in a file nobody renamed
# is not what this is for.
git ls-files -z '*.proto' '*.yaml' '*.yml' 'go.mod' 2>/dev/null | xargs -0 -r cat 2>/dev/null |
	tr -c 'A-Za-z0-9_' '\n' |
	{ grep -E '^[A-Za-z_][A-Za-z0-9_]*$' || true; } |
	sort -u >>"${WORK}/code-names"

# Filenames, because a comment that says "mdheader_test.go covers #57" is
# naming a file that exists, not an identifier that does not. Measured over the
# last twenty commits this one line accounts for three of the four names the
# sweep reported, and every one of them was prose about a real file.
git ls-files | sed 's#.*/##; s#\.[^.]*$##' | sort -u >>"${WORK}/code-names"

sort -u -o "${WORK}/code-names" "${WORK}/code-names"

INDEXED="$(wc -l <"${WORK}/code-names" | tr -d ' ')"

# Does the index look like it came from Go code at all?
#
# If the lexer breaks and routes every line into the comment stream, the index
# comes back nearly empty and the sweep reports every name in the repo as
# stale -- confidently, and in volume. The check has to be scale-free, because
# a magic minimum is wrong for a repo of ten files and wrong for one of ten
# thousand. Every Go file on earth contains `package` and `func` in code, so
# their absence from the code stream is proof the split failed.
GO_FILES="$(git ls-files '*.go' | wc -l | tr -d ' ')"
if [ "${GO_FILES}" -gt 0 ]; then
	for keyword in package func; do
		grep -qx "${keyword}" "${WORK}/code-names" ||
			die_unchecked "the code index has no '${keyword}' across ${GO_FILES} .go files, so the comment/code split failed"
	done
fi

echo ">> indexed ${INDEXED} names across ${GO_FILES} tracked .go files"

# ---------------------------------------------------------------------------
# Independent count of what there was to check.
#
# Cross-check, not decoration. The count below is produced by a dumb grep over
# the raw diff -- it shares no code with the lexer. If the lexer reports zero
# examined comment lines while the grep says there were some to examine, the
# sweep is broken, and saying so is the only honest thing left to do.
# ---------------------------------------------------------------------------
RAW_COMMENT_LINES=0
if [ -n "${CHANGED}" ]; then
	RAW_COMMENT_LINES="$(git diff --unified=0 "${BASE_SHA}" -- '*.go' |
		grep -c -E '^\+.*(//|/\*)' || true)"
fi

# ---------------------------------------------------------------------------
# Collect the comment text on lines the range added, file by file.
# ---------------------------------------------------------------------------
: >"${WORK}/added-comments"

for file in ${CHANGED}; do
	[ -f "${file}" ] || continue

	# Line numbers of added lines, read out of the hunk headers of a zero
	# context diff: "@@ -old,n +new,m @@" means m lines starting at new.
	git diff --unified=0 "${BASE_SHA}" -- "${file}" |
		awk '/^@@/ {
			match($0, /\+[0-9]+(,[0-9]+)?/)
			spec = substr($0, RSTART + 1, RLENGTH - 1)
			split(spec, p, ",")
			count = (p[2] == "" ? 1 : p[2])
			for (k = 0; k < count; k++) print p[1] + k
		}' | sort -un >"${WORK}/added-lines"

	[ -s "${WORK}/added-lines" ] || continue

	awk -f "${WORK}/split.awk" "${file}" |
		awk -F'\t' -v f="${file}" '
			NR == FNR { want[$1] = 1; next }
			$2 == "M" && ($1 in want) && $3 ~ /[A-Za-z]/ { print f "\t" $1 "\t" $3 }
		' "${WORK}/added-lines" - >>"${WORK}/added-comments"
done

EXAMINED="$(wc -l <"${WORK}/added-comments" | tr -d ' ')"

if [ "${EXAMINED}" -eq 0 ] && [ "${RAW_COMMENT_LINES}" -gt 0 ]; then
	die_unchecked "the diff adds ${RAW_COMMENT_LINES} comment line(s) but the sweep examined 0 of them"
fi

if [ "${EXAMINED}" -eq 0 ]; then
	echo ""
	echo ">> the range adds no comment lines to any .go file, confirmed by an"
	echo "   independent count of the raw diff. There is nothing here to sweep."
	exit 0
fi

# ---------------------------------------------------------------------------
# Compare, and report with the evidence attached.
# ---------------------------------------------------------------------------
cut -f3 <"${WORK}/added-comments" | awk -f "${WORK}/shaped.awk" | sort -u >"${WORK}/comment-names"
DISTINCT="$(wc -l <"${WORK}/comment-names" | tr -d ' ')"

comm -23 "${WORK}/comment-names" "${WORK}/code-names" >"${WORK}/stale"
STALE="$(wc -l <"${WORK}/stale" | tr -d ' ')"

echo ">> examined ${EXAMINED} added comment line(s), ${DISTINCT} distinct code-shaped name(s)"

if [ "${STALE}" -eq 0 ]; then
	echo ""
	echo ">> every name in the added comments still exists in code."
	exit 0
fi

echo ""
echo "!! ${STALE} name(s) appear in added comments but nowhere in the tree's code:"
echo "------------------------------------------------------------"
while IFS= read -r name; do
	printf "  %s\n" "${name}"
	grep -Fw -- "${name}" "${WORK}/added-comments" |
		awk -F'\t' '{ sub(/^[ \t]+/, "", $3); printf "      %s:%s  %s\n", $1, $2, $3 }' |
		head -3
done <"${WORK}/stale"
echo "------------------------------------------------------------"
echo "   Each is a name the comment claims exists and the code does not have."
echo "   Usually a rename the comment beside it did not follow. If the name is"
echo "   prose rather than an identifier, reword it -- the comment reads as a"
echo "   reference to code either way, which is the same problem in miniature."
exit 1
