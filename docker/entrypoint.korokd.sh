#!/usr/bin/env bash
set -euo pipefail

JUPYTER_PORT="${JUPYTER_PORT:-44771}"
JUPYTER_TOKEN="${JUPYTER_TOKEN:-agentland-korokd-jupyter}"
JUPYTER_NOTEBOOK_DIR="${JUPYTER_NOTEBOOK_DIR:-/workspace}"
export JUPYTER_HOST="${JUPYTER_HOST:-http://127.0.0.1:${JUPYTER_PORT}}"

JUPYTER_LOG="${JUPYTER_LOG:-/tmp/jupyter.log}"

cleanup() {
  set +e
  if [[ -n "${JUPYTER_PID:-}" ]]; then
    kill "${JUPYTER_PID}" >/dev/null 2>&1 || true
    wait "${JUPYTER_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# Start a local Jupyter server that Korokd can use for stateful kernels.
# It is not exposed outside the Pod; Korokd is the public API surface.
jupyter notebook \
  --ip=127.0.0.1 \
  --port="${JUPYTER_PORT}" \
  --port-retries=0 \
  --allow-root \
  --no-browser \
  --notebook-dir="${JUPYTER_NOTEBOOK_DIR}" \
  --NotebookApp.token="${JUPYTER_TOKEN}" \
  --ServerApp.token="${JUPYTER_TOKEN}" \
  >"${JUPYTER_LOG}" 2>&1 &
JUPYTER_PID=$!

# Wait for readiness (Jupyter requires auth even for /api/kernelspecs).
for _ in {1..60}; do
  if curl -fsS --max-time 1 \
    -H "Authorization: Token ${JUPYTER_TOKEN}" \
    "http://127.0.0.1:${JUPYTER_PORT}/api/kernelspecs" >/dev/null; then
    break
  fi
  sleep 0.2
done

/app/korokd "$@"
