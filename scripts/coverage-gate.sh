#!/usr/bin/env bash
#
# coverage-gate.sh — run the race+coverage test suite and fail the build if any
# package with statements falls below the threshold.
#
# Packages with no test files or no statements (e.g. internal/proto) are treated
# as a pass and skipped, since there is nothing to cover.
#
# Usage: scripts/coverage-gate.sh [threshold]   (threshold default: 80)

set -euo pipefail

THRESHOLD="${1:-80}"

# Run the suite with the race detector and atomic coverage so the profile is
# correct under -race. We capture stdout (the per-package "coverage:" lines)
# while letting the gate parse it; a non-zero `go test` (a real failure) aborts.
echo ">> go test -race -covermode=atomic -coverprofile=coverage.out ./..."
# Disable errexit across the substitution so a failing suite is captured and
# echoed (with set -e, the assignment itself would abort before we can report).
set +e
OUTPUT="$(go test -race -covermode=atomic -coverprofile=coverage.out ./... 2>&1)"
STATUS=$?
set -e
echo "${OUTPUT}"

if [ "${STATUS}" -ne 0 ]; then
	echo ""
	echo "!! go test failed (status ${STATUS}); see output above."
	exit "${STATUS}"
fi

# Parse the per-package results. `go test` emits one of:
#   ok    pkg   0.123s  coverage: 87.5% of statements
#   ok    pkg   0.123s  coverage: 0.0% of statements [no statements]
#   ?     pkg   [no test files]
# We collect failures (covered < threshold) and skips (no statements/tests).
PASS=()
FAIL=()
SKIP=()

while IFS= read -r line; do
	case "${line}" in
		"?"*"[no test files]")
			# "?   pkg   [no test files]"
			pkg="$(echo "${line}" | awk '{print $2}')"
			SKIP+=("${pkg}|no test files")
			;;
		ok*"coverage:"*"[no statements]")
			pkg="$(echo "${line}" | awk '{print $2}')"
			SKIP+=("${pkg}|no statements")
			;;
		ok*"coverage:"*"of statements")
			pkg="$(echo "${line}" | awk '{print $2}')"
			pct="$(echo "${line}" | sed -n 's/.*coverage: \([0-9.]*\)% of statements.*/\1/p')"
			# Compare with awk to avoid relying on bc.
			if awk "BEGIN { exit !(${pct} < ${THRESHOLD}) }"; then
				FAIL+=("${pkg}|${pct}")
			else
				PASS+=("${pkg}|${pct}")
			fi
			;;
	esac
done <<< "${OUTPUT}"

# Print a summary table.
echo ""
echo "Coverage summary (threshold: ${THRESHOLD}%)"
echo "------------------------------------------------------------"
printf "  %-8s %-44s %s\n" "STATUS" "PACKAGE" "COVERAGE"
for entry in "${PASS[@]:-}"; do
	[ -z "${entry}" ] && continue
	printf "  %-8s %-44s %s%%\n" "PASS" "${entry%%|*}" "${entry##*|}"
done
for entry in "${SKIP[@]:-}"; do
	[ -z "${entry}" ] && continue
	printf "  %-8s %-44s %s\n" "SKIP" "${entry%%|*}" "(${entry##*|})"
done
for entry in "${FAIL[@]:-}"; do
	[ -z "${entry}" ] && continue
	printf "  %-8s %-44s %s%% (< %s%%)\n" "FAIL" "${entry%%|*}" "${entry##*|}" "${THRESHOLD}"
done
echo "------------------------------------------------------------"

# Count real failures (the :-} guard yields a single empty element on bash<4.4
# for an empty array, so filter blanks).
fail_count=0
for entry in "${FAIL[@]:-}"; do
	[ -n "${entry}" ] && fail_count=$((fail_count + 1))
done

if [ "${fail_count}" -gt 0 ]; then
	echo "!! ${fail_count} package(s) below ${THRESHOLD}% coverage."
	exit 1
fi

echo ">> all packages with statements meet the ${THRESHOLD}% coverage threshold."
