#!/usr/bin/env bash
set -euo pipefail

CALICO_VERSION="${CALICO_VERSION:-v3.31.1}"
POD_CIDR="${POD_CIDR:-10.244.0.0/16}"

kubectl apply -f "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"

# Calico defaults to 192.168.0.0/16; kind-config.yaml uses 10.244.0.0/16.
kubectl -n kube-system set env daemonset/calico-node "CALICO_IPV4POOL_CIDR=${POD_CIDR}"

# Wait on the DaemonSet, NOT on node readiness. Nodes report Ready as soon as the
# CNI binary is on disk while calico-node is still starting, which would let the
# suite run against a half-ready cluster.
kubectl -n kube-system rollout status daemonset/calico-node --timeout=420s
