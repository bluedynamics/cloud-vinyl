#!/usr/bin/env bash
set -euo pipefail

OPERATOR_IMAGE="${OPERATOR_IMAGE:-ghcr.io/bluedynamics/cloud-vinyl-operator:dev}"
AGENT_IMAGE="${AGENT_IMAGE:-ghcr.io/bluedynamics/cloud-vinyl-agent:dev}"
TRACER_IMAGE="${TRACER_IMAGE:-ghcr.io/bluedynamics/cloud-vinyl-tracer:dev}"
NAMESPACE="${OPERATOR_NAMESPACE:-cloud-vinyl-system}"
# The flavor of the leg: the tracer (and, when set, the daemon) of that
# flavor's catalog entry is replaced by the image under test. The other flavor
# keeps its chart defaults.
FLAVOR="${FLAVOR:-varnish}"
# Optional: a daemon image built from this checkout (Dockerfile.vinyl changed).
DAEMON_IMAGE="${DAEMON_IMAGE:-}"
case "${FLAVOR}" in
  varnish|vinyl) ;;
  *) echo "FLAVOR must be varnish or vinyl, got '${FLAVOR}'" >&2; exit 1 ;;
esac

# Deliberately NOT labeled with vinyl.bluedynamics.eu/operator-namespace=true:
# e2e must exercise the same path a stock `helm install` takes. The operator
# authorizes itself in the agent NetworkPolicy by its own pod IP (issue #58).
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# The tracer under test goes in through the runtime catalog. A values file, not
# --set: the version key contains dots.
VALUES_FILE="$(mktemp)"
trap 'rm -f "${VALUES_FILE}"' EXIT
cat > "${VALUES_FILE}" <<EOF
runtimes:
  ${FLAVOR}:
    versions:
      "9.1.0":
        tracer: "${TRACER_IMAGE}"
EOF
if [ -n "${DAEMON_IMAGE}" ]; then
  echo "        daemon: \"${DAEMON_IMAGE}\"" >> "${VALUES_FILE}"
fi

echo "Installing cloud-vinyl operator (flavor: ${FLAVOR}, image: ${OPERATOR_IMAGE}, agent: ${AGENT_IMAGE}, tracer: ${TRACER_IMAGE})..."
helm upgrade --install cloud-vinyl ./charts/cloud-vinyl \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --set image.operator.repository="${OPERATOR_IMAGE%:*}" \
  --set "image.operator.tag=${OPERATOR_IMAGE##*:}" \
  --set image.agent.repository="${AGENT_IMAGE%:*}" \
  --set "image.agent.tag=${AGENT_IMAGE##*:}" \
  --values "${VALUES_FILE}" \
  --set webhook.certManager.enabled=true \
  --set leaderElection.enabled=true \
  --wait \
  --timeout 120s

echo "cloud-vinyl operator ready in namespace ${NAMESPACE}."
