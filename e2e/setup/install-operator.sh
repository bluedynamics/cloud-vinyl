#!/usr/bin/env bash
set -euo pipefail

OPERATOR_IMAGE="${OPERATOR_IMAGE:-ghcr.io/bluedynamics/cloud-vinyl-operator:dev}"
AGENT_IMAGE="${AGENT_IMAGE:-ghcr.io/bluedynamics/cloud-vinyl-agent:dev}"
NAMESPACE="${OPERATOR_NAMESPACE:-cloud-vinyl-system}"

echo "Installing cloud-vinyl operator (image: ${OPERATOR_IMAGE}, agent: ${AGENT_IMAGE})..."
helm upgrade --install cloud-vinyl ./charts/cloud-vinyl \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --set image.operator.repository="${OPERATOR_IMAGE%:*}" \
  --set "image.operator.tag=${OPERATOR_IMAGE##*:}" \
  --set image.agent.repository="${AGENT_IMAGE%:*}" \
  --set "image.agent.tag=${AGENT_IMAGE##*:}" \
  --set webhook.certManager.enabled=true \
  --set leaderElection.enabled=true \
  --wait \
  --timeout 120s

echo "cloud-vinyl operator ready in namespace ${NAMESPACE}."
