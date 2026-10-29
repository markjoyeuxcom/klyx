#!/bin/zsh
# gates.sh [checkout] - the local gates a slice passes before review: what
# CI runs (.github/workflows/ci.yml) and a little more, on macOS, in one
# report. The checkout defaults to the one holding this script; a worktree
# needs its own cmd/klyx/frontend/node_modules (npm ci, or cp -Rc from the
# main checkout) and a cmd/klyx/frontend/dist (vite build, or a stub file).
#
#   gofmt, the Wails bindings (generated per checkout: $WAILS3, default
#   wails3 on PATH, the version go.mod pins), go vet, the tests, the race
#   detector on the concurrent packages, the PromQL proof module,
#   golangci-lint plain and with the eval tag, tsc, eslint and vitest.
#
# Each step prints its own result; the last line counts the steps that
# failed, and the exit status is that count.
here=${0:A:h}
repo=${1:-${here:h:h}}
t() { perl $here/../lab/smoke/timeout.pl "$@"; }
cd $repo || exit 1
failed=0
step() { # <name> <status>
	(( $2 == 0 )) && print -r -- "$1 ok" || { print -r -- "$1 FAILED ($2)"; failed=$((failed+1)); }
}
print -r -- "## head $(git log --oneline -1) dirty=$(git status --porcelain | wc -l | tr -d ' ')"
# cmd/klyx/build holds generated platform stubs that are not gofmt'd
bad=$(gofmt -l internal cmd/klyxctl cmd/klyx/*.go); print -r -- "## gofmt"; [[ -n $bad ]] && print -r -- $bad; step gofmt ${#bad}
print -r -- "## bindings"
(cd cmd/klyx && t 600 ${WAILS3:-wails3} generate bindings -clean=true -ts > /dev/null 2>&1); step bindings $?
print -r -- "## vet"
go vet ./internal/... ./cmd/klyxctl/... 2>&1 | grep -v '^#' | tail -5; step vet ${pipestatus[1]}
print -r -- "## tests"
t 1500 go test -count=1 ./internal/... ./cmd/klyxctl/... 2>&1 | grep -E '^(FAIL|---|panic)' | head -20; step tests ${pipestatus[1]}
print -r -- "## race"
t 1500 go test -race -count=1 ./internal/appbridge/... ./internal/fleet/... ./internal/fleetindex/... ./internal/policy/... ./internal/observe/... ./internal/healthnotify/... ./internal/obslog/... 2>&1 | grep -E '^(FAIL|---|panic)' | head -20; step race ${pipestatus[1]}
# the PromQL proof is its own module (Settings S3b-2a), where it exists
if [[ -d internal/metrics/promqlproof ]]; then
	print -r -- "## proof"
	(cd internal/metrics/promqlproof && go vet ./... && t 600 go test -count=1 ./... > /dev/null); step proof $?
fi
print -r -- "## lint"
t 900 golangci-lint run ./internal/... ./cmd/klyxctl/... 2>&1 | tail -3; step lint ${pipestatus[1]}
print -r -- "## lint eval"
t 900 golangci-lint run --build-tags eval ./internal/... ./cmd/klyxctl/... 2>&1 | tail -3; step "lint eval" ${pipestatus[1]}
cd cmd/klyx/frontend
print -r -- "## tsc"
t 600 ./node_modules/.bin/tsc --noEmit -p . 2>&1 | tail -3; step tsc ${pipestatus[1]}
print -r -- "## eslint"
t 600 ./node_modules/.bin/eslint src 2>&1 | tail -3; step eslint ${pipestatus[1]}
print -r -- "## vitest"
t 1200 ./node_modules/.bin/vitest run 2>&1 | grep -E 'Test Files|Tests |FAIL' | head -12; step vitest ${pipestatus[1]}
print -r -- "## $failed failed"
exit $failed
