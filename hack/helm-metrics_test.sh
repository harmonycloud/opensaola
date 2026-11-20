#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${repo_root}/chart/opensaola"
render() {
  "${HELM:-helm}" template opensaola "${chart}" "$@"
}

check_render() {
  local address="$1" monitor="$2" docs expected
  shift 2
  docs="$(render "$@" | awk '/^# Source: / {keep = $0 ~ /\/templates\/(deployment|servicemonitor)\.yaml$/} keep')"
  awk -v arg="--metrics-bind-address=${address}" '$1 == "-" && $2 == arg {found=1} END {exit !found}' <<<"${docs}" || {
    echo "Expected metrics bind address ${address} for: $*" >&2; exit 1
  }
  local port_count
  port_count="$(awk '/^ *- name: metrics$/ && deployment {n++} /^# Source:/ {deployment = /\/deployment\.yaml$/} END {print n+0}' <<<"${docs}")"
  expected=1
  if [[ "${address}" == 0 ]]; then expected=0; fi
  [[ "${port_count}" == "${expected}" ]] || { echo "Unexpected Deployment metrics port for: $*" >&2; exit 1; }
  local kind count
  for kind in Service ServiceMonitor; do
    count="$(awk -v kind="${kind}" '$1 == "kind:" && $2 == kind {n++} END {print n+0}' <<<"${docs}")"
    [[ "${count}" == "${monitor}" ]] || { echo "Unexpected ${kind} count for: $*" >&2; exit 1; }
  done
}

check_rejected() {
  local diagnostic="$1" output
  shift
  if output="$(render "$@" 2>&1)"; then
    echo "Unexpected successful render for: $*" >&2; exit 1
  fi
  # Helm 3 uses dotted schema paths and Helm 4 uses JSON pointers.
  [[ "${output//\//.}" == *"${diagnostic}"* ]] || { echo "${output}" >&2; exit 1; }
}

check_render :8081 0
check_render :8081 0 -f - <<< 'replicas: 1'
check_render :8081 0 --set metrics.enabled=true,metrics.serviceMonitor.enabled=false
check_render :8081 1 --set metrics.enabled=true,metrics.serviceMonitor.enabled=true
check_render 0 0 --set metrics.enabled=false,metrics.serviceMonitor.enabled=false
check_rejected 'metrics.serviceMonitor.enabled=true requires metrics.enabled=true' --set metrics.enabled=false,metrics.serviceMonitor.enabled=true
check_rejected 'metrics.enabled' --set-string metrics.enabled=false
check_rejected 'metrics.serviceMonitor.enabled' --set-string metrics.serviceMonitor.enabled=false
