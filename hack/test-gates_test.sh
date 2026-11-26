#!/usr/bin/env bash
# Exercise exit codes and the per-gate record without starting any services.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/root/scripts" "$tmp/root/hack" "$tmp/root/bin" "$tmp/reports"
# The runner resolves paths relative to itself, exactly as in a checkout.
cp scripts/test_all_gates.sh "$tmp/root/scripts"
git -C "$tmp/root" init -q
git -C "$tmp/root" -c user.name=test -c user.email=test@example.org commit -q --allow-empty -m fixture
cat >"$tmp/root/bin/make" <<'STUB'
#!/bin/sh
echo "make $1"
[ "$1" != "${FAIL_GATE:-}" ]
STUB
printf '#!/bin/sh\nexit 0\n' >"$tmp/root/bin/go"
chmod +x "$tmp"/root/bin/*
printf 'exit 0\n' >"$tmp/root/hack/test-helm-upgrade.sh"
export TEST_REPORT_DIR="$tmp/reports"
unset MIDDLEWARE_FIXTURES
check() {
  local expected=$1 profile=$2 actual=0
  bash "$tmp/root/scripts/test_all_gates.sh" "$profile" >"$tmp/output" 2>&1 || actual=$?
  [[ $actual == "$expected" ]] || { cat "$tmp/output"; exit 1; }
  summary=$(sed -n 's/^Test report: //p' "$tmp/output")/summary.tsv
  test -s "$summary"
}
check 0 pr
awk -F '\t' 'NR>1 && $2 != "PASS" {exit 1} END {if(NR != 10) exit 1}' "$summary"
check 77 release
grep -q $'^original-packages\tBLOCKED\t77\t' "$summary"
export FAIL_GATE=test
check 1 release
grep -q $'^unit\tFAIL\t1\t' "$summary"
grep -q $'^FuzzDeclaredEditLifecycle\tPASS\t0\t' "$summary"
echo "PASS: successful, failed and blocked gates retain logs and return the correct status"
