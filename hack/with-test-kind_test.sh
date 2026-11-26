#!/usr/bin/env bash
# No Kubernetes or container runtime is used by these regression tests.
set -euo pipefail
runner=$(cd "$(dirname "$0")" && pwd)/with-test-kind.sh
sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "$sandbox/bin" "$sandbox/tmp"
export TMPDIR="$sandbox/tmp" PATH="$sandbox/bin:$PATH"
export KIND_BIN="$sandbox/bin/kind" KIND_CLUSTER=existing KUBECONFIG="$sandbox/existing"
export TRACE="$sandbox/trace"
echo untouched > "$KUBECONFIG"

cat > "$KIND_BIN" <<'STUB'
#!/usr/bin/env bash
set -eu
[[ "$KIND_CLUSTER" != existing && "$KUBECONFIG" == "$TMPDIR/"* ]]
case "$1 $2" in
  'get clusters')
    echo existing
    case "$SCENARIO" in collision) echo "$KIND_CLUSTER";; list-error) exit 23;; esac ;;
  'create cluster')
    [[ "$3 $4 $5 $6" == "--name $KIND_CLUSTER --kubeconfig $KUBECONFIG" ]]
    echo create >> "$TRACE"
    [[ "$SCENARIO" != partial ]] || exit 24
    echo "kind-$KIND_CLUSTER" > "$KUBECONFIG" ;;
  'delete cluster')
    [[ "$3 $4" == "--name $KIND_CLUSTER" ]]
    echo delete >> "$TRACE"
    case "$SCENARIO" in cleanup-error|blocked-and-cleanup-error) exit 25;; esac ;;
  *) exit 97 ;;
esac
STUB
cat > "$sandbox/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
set -eu
[[ "$*" == 'config current-context' ]]
if [[ "$SCENARIO" == wrong-context ]]; then echo kind-existing; else cat "$KUBECONFIG"; fi
STUB
cat > "$sandbox/bin/check" <<'STUB'
#!/usr/bin/env bash
set -eu
[[ $(cat "$KUBECONFIG") == "kind-$KIND_CLUSTER" && "$1" == "two words" ]]
echo check >> "$TRACE"
case "$SCENARIO" in
    failed) exit 26 ;;
    blocked-and-cleanup-error) exit 76 ;;
    interrupt) kill -TERM "$PPID"; exec sleep 30 ;;
esac
STUB
chmod +x "$sandbox/bin/"*
test_case() {
  local expected_status="$2" expected_trace="$3" rc=0
  export SCENARIO="$1"
  : > "$TRACE"
  bash "$runner" check 'two words' || rc=$?
  if [[ "$rc" != "$expected_status" || $(paste -sd ',' "$TRACE") != "$expected_trace" ]]; then
    echo "Unexpected result for $SCENARIO: status=$rc commands=$(cat "$TRACE")" >&2; exit 1
  fi
  [[ -z $(ls -A "$TMPDIR") && $(cat "$KUBECONFIG") == untouched ]]
  echo "PASS: $SCENARIO"
}
test_case success 0 create,check,delete
test_case list-error 23 ''
test_case collision 1 ''
test_case partial 24 create,delete
test_case wrong-context 1 create,delete
test_case failed 26 create,check,delete
test_case cleanup-error 1 create,check,delete
test_case blocked-and-cleanup-error 1 create,check,delete
test_case interrupt 143 create,check,delete
