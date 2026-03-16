from __future__ import annotations

"""deepagents skills 支持。"""

from collections import OrderedDict
from pathlib import Path, PurePosixPath
from typing import cast

from langchain_core.messages import AnyMessage, HumanMessage, SystemMessage

from app.services.skills_config import resolve_skill_sources

try:
    from deepagents.backends.filesystem import FilesystemBackend
    from deepagents.middleware.skills import SKILLS_SYSTEM_PROMPT, SkillMetadata, _list_skills
except ImportError:  # pragma: no cover - exercised only when dependency is absent
    FilesystemBackend = None
    SkillMetadata = dict[str, object]  # type: ignore[misc,assignment]
    SKILLS_SYSTEM_PROMPT = ""
    _list_skills = None


def build_skills_prompt(workspace_path: Path) -> str:
    """构造 deepagents 风格的 skills 系统提示词。"""

    if FilesystemBackend is None or _list_skills is None:
        return ""

    sources = resolve_skill_sources(workspace_path)
    if not sources:
        return ""

    backend = FilesystemBackend(root_dir="/", virtual_mode=False)
    all_skills: OrderedDict[str, SkillMetadata] = OrderedDict()

    for source in sources:
        for skill in _list_skills(backend, source):
            skill_name = str(skill["name"])
            if skill_name in all_skills:
                del all_skills[skill_name]
            all_skills[skill_name] = skill

    if not all_skills:
        return ""

    return SKILLS_SYSTEM_PROMPT.format(
        skills_locations=_format_skills_locations(sources),
        skills_list=_format_skills_list(list(all_skills.values())),
    ).strip()


def inject_skills_into_messages(messages: list[AnyMessage], workspace_path: Path) -> list[AnyMessage]:
    """将 skills 说明注入到消息列表的 system prompt。"""

    skills_prompt = build_skills_prompt(workspace_path)
    if not skills_prompt:
        return list(messages)

    out = list(messages)
    if out and isinstance(out[0], SystemMessage):
        out[0] = SystemMessage(content=_append_text_block(out[0].content, skills_prompt))
        return out

    return [SystemMessage(content=skills_prompt), *out]


def build_skill_aware_system_prompt(system_prompt: str, workspace_path: Path) -> str:
    """将 skills 说明附加到已有 system prompt 字符串。"""

    skills_prompt = build_skills_prompt(workspace_path)
    if not skills_prompt:
        return system_prompt
    return f"{system_prompt}\n\n{skills_prompt}"


def _format_skills_locations(sources: list[str]) -> str:
    lines: list[str] = []
    for index, source in enumerate(sources):
        if not Path(source).exists():
            continue
        name = PurePosixPath(source.rstrip("/")).name.capitalize()
        suffix = " (higher priority)" if index == len(sources) - 1 else ""
        lines.append(f"**{name} Skills**: `{source}`{suffix}")
    return "\n".join(lines)


def _format_skills_list(skills: list[SkillMetadata]) -> str:
    if not skills:
        return "(No skills available)"

    lines: list[str] = []
    for raw_skill in skills:
        skill = cast(dict[str, object], raw_skill)
        lines.append(f"- **{skill['name']}**: {skill['description']}")
        allowed_tools = skill.get("allowed_tools")
        if isinstance(allowed_tools, list) and allowed_tools:
            lines.append(f"  -> Allowed tools: {', '.join(str(item) for item in allowed_tools)}")
        lines.append(f"  -> Read `{skill['path']}` for full instructions")
    return "\n".join(lines)


def _append_text_block(content: object, extra: str) -> object:
    if isinstance(content, str):
        return f"{content}\n\n{extra}"
    if isinstance(content, list):
        out = list(content)
        out.append({"type": "text", "text": extra})
        return out
    return f"{content}\n\n{extra}"
