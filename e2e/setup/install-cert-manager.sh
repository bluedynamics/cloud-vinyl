#!/usr/bin/env bash
set -euo pipefail

echo "Installing cert-manager..."
helm repo add jetstack https://charts.jetstack.io --force-update
helm repo update
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set installCRDs=true \
  --wait \
  --timeout 120s

echo "cert-manager ready."
