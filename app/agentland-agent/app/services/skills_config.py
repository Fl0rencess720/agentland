from __future__ import annotations

"""deepagents skills 路径配置。"""

import json
import os
from pathlib import Path

SKILL_SOURCES_ENV = "AGENTLAND_AGENT_SKILL_SOURCES"


def resolve_skill_sources(workspace_path: Path) -> list[str]:
    """返回当前工作区可用的 deepagents skill 源目录。"""

    sources: list[Path] = [
        Path.home() / ".deepagents" / "agent" / "skills",
        workspace_path / ".deepagents" / "skills",
    ]

    raw = os.getenv(SKILL_SOURCES_ENV)
    if raw:
        for item in _parse_env_sources(raw, workspace_path):
            sources.append(item)

    out: list[str] = []
    seen: set[str] = set()
    for path in sources:
        resolved = path.expanduser().resolve()
        value = resolved.as_posix()
        if value in seen:
            continue
        seen.add(value)
        out.append(value)
    return out


def _parse_env_sources(raw: str, workspace_path: Path) -> list[Path]:
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        parsed = [item for item in raw.split(os.pathsep) if item.strip()]

    if not isinstance(parsed, list):
        raise ValueError(f"{SKILL_SOURCES_ENV} must be a JSON array or path-separated string")

    sources: list[Path] = []
    for item in parsed:
        if not isinstance(item, str) or not item.strip():
            continue
        expanded = item.replace("{workspace}", workspace_path.as_posix())
        sources.append(Path(expanded))
    return sources
