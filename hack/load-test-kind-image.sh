#!/usr/bin/env bash
# Load an already-local image onto each node of the specified test cluster.
set -euo pipefail
cluster=${1:?expected a Kind cluster name}
image=${2:?expected a local image}
kind_bin=${KIND_BIN:-kind}
if "$kind_bin" load docker-image --name "$cluster" "$image"; then exit 0; fi
# Older Kind versions cannot autodetect the snapshotter on newer node images.
nodes=$("$kind_bin" get nodes --name "$cluster")
[[ -n "$nodes" ]] || { echo "No Kind nodes for $cluster" >&2; exit 1; }
platform=$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")
while IFS= read -r node; do
  docker save --platform "$platform" "$image" | docker exec -i "$node" ctr -n k8s.io images import --local --platform "$platform" -
done <<< "$nodes"
