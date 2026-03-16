from __future__ import annotations

"""deepagents skills integration tests."""

import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage

pytest.importorskip("deepagents")

from app.main import app
from app.models.ralph import RalphPrd
from app.services.skills_service import build_skills_prompt


def _run_dir(workspace: Path, session_id: str, run_id: str = "run-0001") -> Path:
    return workspace / ".ralph" / session_id / run_id


def test_build_skills_prompt_lists_project_skill(tmp_path: Path) -> None:
    """Project skills should be exposed in the injected system prompt."""

    skill_dir = tmp_path / ".deepagents" / "skills" / "web-research"
    skill_dir.mkdir(parents=True)
    (skill_dir / "SKILL.md").write_text(
        """---
name: web-research
description: Use this skill when the user asks for structured web research.
---
# Web research skill
Read this skill before researching.
""",
        encoding="utf-8",
    )

    prompt = build_skills_prompt(tmp_path)

    assert "Skills System" in prompt
    assert "web-research" in prompt
    assert "structured web research" in prompt
    assert str((skill_dir / "SKILL.md").resolve()) in prompt


def test_chat_stream_injects_skill_prompt(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Chat branch should inject deepagents skill metadata into the system prompt."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    workspace = tmp_path / "workspace"
    skill_dir = workspace / ".deepagents" / "skills" / "repo-audit"
    skill_dir.mkdir(parents=True)
    (skill_dir / "SKILL.md").write_text(
        """---
name: repo-audit
description: Use this skill when auditing repository changes.
---
# Repo audit skill
Follow the audit checklist.
""",
        encoding="utf-8",
    )

    client = TestClient(app)

    def fake_route_prompt(*, messages, api_key: str, model: str, base_url: str | None, timeout: float) -> str:  # noqa: ANN001
        return "chat"

    def fake_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        assert isinstance(messages[0], SystemMessage)
        rendered = str(messages[0].content)
        assert "Skills System" in rendered
        assert "repo-audit" in rendered
        reply = AIMessage(content="done")
        if cfg.hooks.on_assistant is not None:
            cfg.hooks.on_assistant(reply)
        return [*messages, reply]

    monkeypatch.setattr("app.services.chat_service.route_prompt", fake_route_prompt)
    monkeypatch.setattr("app.services.chat_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.chat_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": "skill-chat",
            "workspace_path": str(workspace),
            "message": "帮我看一下仓库变更",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"mode": "chat"' in body


def test_ralph_stream_injects_skill_prompt(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Ralph iteration prompt should include deepagents skill metadata."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    skill_dir = tmp_path / ".deepagents" / "skills" / "migration"
    skill_dir.mkdir(parents=True)
    (skill_dir / "SKILL.md").write_text(
        """---
name: migration
description: Use this skill when planning or applying schema migrations.
---
# Migration skill
Follow the migration checklist.
""",
        encoding="utf-8",
    )

    client = TestClient(app)

    def fake_plan_prd_from_requirement(*, request, run_state, api_key):  # noqa: ANN001
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/migration-check",
                "description": "Check migration skill prompt",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Check migration skill prompt",
                        "description": "Ensure the prompt includes the migration skill",
                        "acceptanceCriteria": ["Prompt contains migration skill metadata"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        assert isinstance(messages[0], SystemMessage)
        rendered = str(messages[0].content)
        assert "Skills System" in rendered
        assert "migration" in rendered
        run_dir = _run_dir(tmp_path, "skill-ralph")
        payload = RalphPrd.model_validate_json((run_dir / "prd.json").read_text(encoding="utf-8")).model_dump(mode="json")
        payload["userStories"][0]["passes"] = True
        (run_dir / "prd.json").write_text(
            json.dumps(payload, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": "skill-ralph",
            "workspace_path": str(tmp_path),
            "requirement": "Check the migration skill prompt.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body
