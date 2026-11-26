#!/usr/bin/env bash
# Run every required gate in a profile, preserving per-attempt status and output.
set -euo pipefail
trap 'exit 130' INT
trap 'exit 143' TERM
umask 077
cd "$(dirname "$0")/.."
profile=${1:-full}
case "$profile" in pr|release|performance|full) ;; *)
  echo "Usage: $0 [pr|release|performance|full]" >&2; exit 2;;
esac
[[ $# -le 1 ]] || { echo "Expected one profile" >&2; exit 2; }
export PATH="$PWD/bin:$PATH"
report_parent=${TEST_REPORT_DIR:-"${TMPDIR:-/tmp}"}
mkdir -p "$report_parent"
report=$(mktemp -d "$report_parent/opensaola-$profile.XXXXXXXX")
summary="$report/summary.tsv"
printf 'gate\tresult\texit\tlog\n' >"$summary"
echo "Profile: $profile. Test report: $report"
git rev-parse HEAD > "$report/commit"
git status --short > "$report/worktree"
failed=0
blocked=0
run() {
  local name=$1 rc=0 result=PASS
  shift
  echo "Running $name"
  "$@" > "$report/$name.log" 2>&1 || rc=$?
  if ((rc == 130 || rc == 143)); then
    printf '%s\tINTERRUPTED\t%d\t%s.log\n' "$name" "$rc" "$name" >> "$summary"
    exit "$rc"
  elif ((rc == 77)); then
    result=BLOCKED; blocked=1
  elif ((rc != 0)); then
    result=FAIL; failed=1
  fi
  if ((rc != 0)); then tail -n 8 "$report/$name.log" >&2; fi
  printf '%s\t%s\t%d\t%s.log\n' "$name" "$result" "$rc" "$name" >> "$summary"
  echo "$name: $result"
}
matrix() {
  if [[ -z "${MIDDLEWARE_FIXTURES:-}" || ! -d "$MIDDLEWARE_FIXTURES" ]]; then
    echo "Set MIDDLEWARE_FIXTURES to the original-package fixture directory"; return 77
  fi
  local rc=0
  python3 scripts/test-middleware-matrix.py --fixtures "$MIDDLEWARE_FIXTURES" --report "$report/middleware.json" || rc=$?
  if [[ $rc == 2 ]]; then return 77; fi
  return "$rc"
}
if [[ "$profile" != performance ]]; then
  run unit make test check-coverage
  run race make test-race
  run lint make lint
  if [[ "$profile" == pr ]]; then
    run api make test-envtest
  else
    run api make test-envtest ENVTEST_FLAGS=-race
  fi
  run helm make helm-check
  run build make build
  run smoke make test-e2e-smoke
  run redis make test-fields-redis
  run catalog-retention make test-helm-data-namespace
fi
if [[ "$profile" == release || "$profile" == full ]]; then
  run helm-upgrade bash hack/test-helm-upgrade.sh
  run original-packages matrix
  for fuzz in FuzzDeclaredPlanFieldIdentity FuzzDeclaredEditLifecycle; do
    run "$fuzz" go test ./internal/k8s -run '^$' -fuzz "^$fuzz$" -fuzztime "${FUZZ_TIME:-45s}"
  done
fi
if [[ "$profile" == performance || "$profile" == full ]]; then
  run benchmarks make bench
  run field-write-comparison bash hack/compare-field-performance.sh
fi
cat "$summary"
echo "Test report: $report"
if ((failed)); then exit 1; fi
if ((blocked)); then exit 77; fi
