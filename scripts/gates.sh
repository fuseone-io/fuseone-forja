#!/usr/bin/env bash
# Runs the gates of `make check` one by one, every gate even after a red one,
# and prints one line per gate with its real exit code and the commit it ran
# on. Exits 0 only when every gate exited 0.
#
#   scripts/gates.sh                  # build vet lint test race docker-test
#   scripts/gates.sh build vet test   # a subset, in the order given
#
# GATES_LOG_DIR keeps the raw logs (default: a fresh temporary directory).
# GOLANGCI_LINT points make lint at a binary that is not on PATH.
set -u

cd "$(git rev-parse --show-toplevel)" || exit 2

if [ "$#" -eq 0 ]; then
	set -- build vet lint test race docker-test
fi

log_dir="${GATES_LOG_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/forja-gates.XXXXXX")}"
mkdir -p "$log_dir" || exit 2

sha="$(git rev-parse HEAD)"
dirty=""
if ! git diff --quiet HEAD -- '*.go' go.mod go.sum Makefile; then
	dirty=" +uncommitted"
fi

failed=0
rows=""
for gate in "$@"; do
	started=$SECONDS
	make --no-print-directory "$gate" >"$log_dir/$gate.log" 2>&1
	code=$?
	rows+="$(printf '%-12s %4d %5ds  %s' "$gate" "$code" $((SECONDS - started)) "$log_dir/$gate.log")"$'\n'
	if [ "$code" -ne 0 ]; then
		failed=1
	fi
done

printf 'sha %s%s\n' "$sha" "$dirty"
printf '%-12s %4s %6s  %s\n' gate exit time log
printf '%s' "$rows"
exit "$failed"
