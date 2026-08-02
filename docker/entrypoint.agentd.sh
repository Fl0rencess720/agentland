#!/usr/bin/env bash
set -euo pipefail

mkdir -p "${AL_AGENTD_WORKSPACE_ROOT:-/workspace}/.agentland"
exec /app/agentd "$@"

