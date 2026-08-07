#!/usr/bin/env bash
set -euo pipefail

workspace="${AL_AGENTD_WORKSPACE_ROOT:-/workspace}"
tool_uid="${AL_AGENTD_TOOL_UID:-10001}"
tool_gid="${AL_AGENTD_TOOL_GID:-10001}"

if [[ ! "${tool_uid}" =~ ^[1-9][0-9]*$ || ! "${tool_gid}" =~ ^[1-9][0-9]*$ ]]; then
  echo "AL_AGENTD_TOOL_UID and AL_AGENTD_TOOL_GID must be positive integers" >&2
  exit 1
fi

mkdir -p "${workspace}"
chown -R "${tool_uid}:${tool_gid}" "${workspace}"
chmod -R u+rwX "${workspace}"
chmod 2770 "${workspace}"

if [[ -L "${workspace}/.agentland" ]]; then
  rm -- "${workspace}/.agentland"
elif [[ -e "${workspace}/.agentland" && ! -d "${workspace}/.agentland" ]]; then
  echo "${workspace}/.agentland must be a directory" >&2
  exit 1
fi
mkdir -p "${workspace}/.agentland"
chown -R root:root "${workspace}/.agentland"
chmod -R u+rwX,go-rwx "${workspace}/.agentland"
exec /app/agentd "$@"
