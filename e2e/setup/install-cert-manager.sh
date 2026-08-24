#!/usr/bin/env bash
set -euo pipefail

# Pinned so CI stops installing whatever is latest on the day. Unpinned, a run
# can change behaviour with no change to this repo (#63). v1.21.1 is what the
# last known-good runs installed.
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.21.1}"

echo "Installing cert-manager ${CERT_MANAGER_VERSION}..."
helm repo add jetstack https://charts.jetstack.io --force-update
helm repo update
# --wait alone only waits for Deployments to become Available, not for the
# webhook to serve traffic. The chart's own startupapicheck Job covers that gap
# and --wait does wait for it, so no extra probe is needed here.
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --version "${CERT_MANAGER_VERSION}" \
  --set installCRDs=true \
  --wait \
  --timeout 120s

echo "cert-manager ready."
