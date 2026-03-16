from __future__ import annotations

"""Ralph orchestration tests."""

import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage

pytest.importorskip("langgraph")

from app.main import app
from app.models.ralph import RalphPrd
from app.schemas.ralph import RalphStreamRequest
from app.services.session_memory import SessionManager


def _run_dir(workspace: Path, session_id: str, run_id: str = "run-0001") -> Path:
    return workspace / ".ralph" / session_id / run_id


def test_ralph_stream_runs_fresh_iterations(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Each Ralph iteration should start the agent with fresh chat context."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")

    client = TestClient(app)
    session_id = "ralph-test"
    run_dir = _run_dir(tmp_path, session_id)
    prd_path = run_dir / "prd.json"
    calls: list[list[object]] = []

    def fake_plan_prd_from_requirement(*, request, run_state, api_key):  # noqa: ANN001
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/test-feature",
                "description": "Test feature",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Implement test feature",
                        "description": "Make the test pass",
                        "acceptanceCriteria": ["Write code", "Run checks"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        calls.append(messages)
        assert len(messages) == 2
        assert isinstance(messages[0], SystemMessage)
        assert isinstance(messages[1], HumanMessage)
        assert "Ralph iteration" in str(messages[1].content)

        if len(calls) < 3:
            return [*messages, AIMessage(content=f"iteration {len(calls)} incomplete")]

        payload = json.loads(prd_path.read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        prd_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(tmp_path),
            "requirement": "Build a test feature.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert "event: session" in body
    assert "event: plan_ready" in body
    assert "event: iteration_start" in body
    assert '"status": "complete"' in body
    assert len(calls) == 3
    assert all(len(call) == 2 for call in calls)
    assert "Ralph iteration 1 of 10." in str(calls[0][1].content)
    assert "Ralph iteration 2 of 10." in str(calls[1][1].content)
    assert "Ralph iteration 3 of 10." in str(calls[2][1].content)
    assert prd_path.exists()
    assert (run_dir / "progress.txt").exists()


def test_ralph_stream_resumes_existing_run(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Existing Ralph file state should be reused without replanning."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")

    client = TestClient(app)
    session_id = "ralph-resume"
    run_dir = _run_dir(tmp_path, session_id)
    run_dir.mkdir(parents=True)
    (run_dir / "prd.json").write_text(
        json.dumps(
            {
                "project": "demo",
                "branchName": "ralph/existing-run",
                "description": "Existing run",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Continue work",
                        "description": "Resume existing plan",
                        "acceptanceCriteria": ["Finish work"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    (run_dir / "progress.txt").write_text("# Ralph Progress Log\nStarted: now\n---\n", encoding="utf-8")

    def fail_if_called(*args, **kwargs):  # noqa: ANN002,ANN003
        raise AssertionError("planner should not be called when prd.json already exists")

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        (tmp_path / "done.txt").write_text("done\n", encoding="utf-8")
        (run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fail_if_called)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(tmp_path),
            "requirement": "Resume the existing run.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body


def test_ralph_stream_starts_new_run_after_previous_run_completed(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """A completed run should not block a new requirement in the same chat session."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")

    client = TestClient(app)
    session_id = "ralph-multi-run"
    first_run_dir = _run_dir(tmp_path, session_id, "run-0001")
    second_run_dir = _run_dir(tmp_path, session_id, "run-0002")
    first_run_dir.mkdir(parents=True)
    (first_run_dir / "prd.json").write_text(
        json.dumps(
            {
                "project": "demo",
                "branchName": "ralph/first-run",
                "description": "Completed first run",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Finish first run",
                        "description": "Already complete",
                        "acceptanceCriteria": ["Work is done"],
                        "priority": 1,
                        "passes": True,
                        "notes": "",
                    }
                ],
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    (first_run_dir / "progress.txt").write_text(
        "# Ralph Progress Log\nStarted: now\n---\n## 2026-03-15T00:00:00 - US-001\n- Completed first run.\n---\n",
        encoding="utf-8",
    )
    planner_calls: list[str] = []

    def fake_plan_prd_from_requirement(*, request, run_state, api_key):  # noqa: ANN001
        planner_calls.append(run_state.run_id)
        assert run_state.run_id == "run-0002"
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/second-run",
                "description": "Plan a fresh second run",
                "userStories": [
                    {
                        "id": "US-002",
                        "title": "Handle new requirement",
                        "description": "Run a new task instead of reusing the old PRD",
                        "acceptanceCriteria": ["New run finishes successfully"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        payload = json.loads((second_run_dir / "prd.json").read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        (second_run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(tmp_path),
            "requirement": "Start a different task after the first run is done.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body
    assert planner_calls == ["run-0002"]
    assert str(second_run_dir) in body
    assert (second_run_dir / "prd.json").exists()


def test_ralph_stream_falls_back_when_planner_fails(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Planner failures should not prevent the Ralph loop from starting."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")

    client = TestClient(app)
    session_id = "ralph-fallback"
    run_dir = _run_dir(tmp_path, session_id)

    def fail_planner(*, request, run_state, api_key):  # noqa: ANN001
        raise RuntimeError("'str' object has no attribute 'error'")

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
        assert payload["userStories"][0]["notes"] == "Fallback PRD generated because automatic planning failed."
        payload["userStories"][0]["passes"] = True
        (tmp_path / "hello.txt").write_text("hello\n", encoding="utf-8")
        (run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_with_model", fail_planner)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(tmp_path),
            "requirement": "Create hello.txt and log progress.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body
    assert (run_dir / "prd.json").exists()
    assert "event: planner_fallback" in body
    assert "RuntimeError: 'str' object has no attribute 'error'" in body


def test_plan_prd_with_model_uses_streaming(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Planner should use streaming mode for gateways that require stream=true."""

    captured: dict[str, object] = {}

    class FakeChunk:
        def __init__(self, content: str) -> None:
            self.content = content
            self.tool_calls: list[object] = []
            self.additional_kwargs: dict[str, object] = {}
            self.response_metadata: dict[str, object] = {}
            self.id = None

        def __add__(self, other: "FakeChunk") -> "FakeChunk":
            return FakeChunk(self.content + other.content)

    class FakePlanner:
        def __init__(self, **kwargs: object) -> None:
            captured["kwargs"] = kwargs

        def stream(self, messages: list[object]) -> list[FakeChunk]:
            captured["messages"] = messages
            return [
                FakeChunk('{"project":"demo","branchName":"ralph/test","description":"d",'),
                FakeChunk('"userStories":[{"id":"US-001","title":"t","description":"d","acceptanceCriteria":["a"],"priority":1,"passes":false,"notes":""}]}'),
            ]

    monkeypatch.setattr("app.services.ralph_service.ChatOpenAI", FakePlanner)

    from app.models.ralph import RalphRunState
    from app.services.ralph_service import _plan_prd_with_model

    prd = _plan_prd_with_model(
        request=RalphStreamRequest(requirement="test requirement"),
        run_state=RalphRunState(
            session_id="test",
            run_id="run-0001",
            workspace_path=tmp_path,
            session_root=tmp_path / ".ralph" / "test",
            run_dir=tmp_path / ".ralph" / "test" / "run-0001",
            prd_path=tmp_path / ".ralph" / "test" / "run-0001" / "prd.json",
            progress_path=tmp_path / ".ralph" / "test" / "run-0001" / "progress.txt",
        ),
        api_key="test-key",
        project_name="demo",
    )

    assert prd.branchName == "ralph/test"
    assert captured["kwargs"]["streaming"] is True
    assert captured["kwargs"]["use_responses_api"] is False


def test_ralph_stream_reconciles_progress_and_skill_stories(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Workspace artifacts should reconcile bookkeeping stories to passed."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")

    client = TestClient(app)
    session_id = "ralph-reconcile"
    run_dir = _run_dir(tmp_path, session_id)

    def fake_plan_prd_from_requirement(*, request, run_state, api_key):  # noqa: ANN001
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/reconcile",
                "description": "Create a file and record progress with a skill",
                "userStories": [
                    {
                        "id": "US1",
                        "title": "Create ralph-skill.txt",
                        "description": "Create a file named ralph-skill.txt with the requested content.",
                        "acceptanceCriteria": [
                            "A file named ralph-skill.txt exists at the repository root",
                            'The file content equals exactly: "ralph skill ok"',
                        ],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    },
                    {
                        "id": "US2",
                        "title": "Update Ralph progress log",
                        "description": "Update the Ralph progress log to record the work.",
                        "acceptanceCriteria": [
                            "Ralph progress log contains a new entry mentioning creation of ralph-skill.txt and use of progress-note skill",
                            "Log entry is appended (not overwriting prior entries)",
                        ],
                        "priority": 2,
                        "passes": False,
                        "notes": "",
                    },
                    {
                        "id": "US3",
                        "title": "Record progress using progress-note skill",
                        "description": "Use the progress-note skill to add a progress note about the work performed.",
                        "acceptanceCriteria": [
                            "A progress note is created via the progress-note skill describing the created file and log update"
                        ],
                        "priority": 3,
                        "passes": False,
                        "notes": "",
                    },
                ],
            }
        )

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        target = tmp_path / "ralph-skill.txt"
        target.write_text("ralph skill ok\n", encoding="utf-8")

        progress_path = run_dir / "progress.txt"
        progress_path.write_text(
            "# Ralph Progress Log\n"
            "Started: now\n"
            "---\n"
            "## 2026-03-12T18:39:30 - US1\n"
            "- Created ralph-skill.txt with content \"ralph skill ok\".\n"
            "- Files changed:\n"
            "  - ralph-skill.txt\n"
            "- Learnings for future iterations:\n"
            "  - Patterns discovered: learned from progress-note skill\n"
            "  - Gotchas encountered: None\n"
            "  - Useful context: Initial story creation.\n"
            "---\n",
            encoding="utf-8",
        )

        skill_dir = tmp_path / ".deepagents" / "skills" / "progress-note"
        skill_dir.mkdir(parents=True, exist_ok=True)
        (skill_dir / "SKILL.md").write_text(
            """---
name: progress-note
description: Use this skill when the user asks you to use the progress-note skill.
---
""",
            encoding="utf-8",
        )
        (skill_dir / "notes.log").write_text(
            "2026-03-12T18:39:30 - Created ralph-skill.txt and updated progress log.\n",
            encoding="utf-8",
        )
        return [*messages, AIMessage(content="work completed")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(tmp_path),
            "requirement": "Create ralph-skill.txt and log progress with the progress-note skill.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body
    payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
    assert [story["passes"] for story in payload["userStories"]] == [True, True, True]


def test_ralph_planner_receives_prior_session_memory(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Planner input should include prior persisted chat memory."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="ralph-memory",
        system_prompt="You are persistent.",
    )
    manager.append_message(HumanMessage(content="Earlier chat preference"))
    manager.append_message(AIMessage(content="I will remember this context."))

    client = TestClient(app)
    run_dir = _run_dir(workspace, "ralph-memory")
    captured: dict[str, str] = {}

    def fake_plan_prd_from_requirement(*, request, run_state, api_key, planner_memory=""):  # noqa: ANN001
        captured["planner_memory"] = planner_memory
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/memory-aware",
                "description": "Memory aware task",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Finish the task",
                        "description": "Use prior memory during planning",
                        "acceptanceCriteria": ["Task is completed"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        (run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/ralph/stream",
        json={
            "session_id": "ralph-memory",
            "workspace_path": str(workspace),
            "requirement": "Use my prior context to plan this task.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body
    assert "Earlier chat preference" in captured["planner_memory"]
    assert "I will remember this context." in captured["planner_memory"]


def test_ralph_writes_result_back_to_session_memory(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Completed Ralph runs should append a result summary to persistent memory."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    client = TestClient(app)
    session_id = "ralph-memory-result"
    run_dir = _run_dir(workspace, session_id)

    def fake_plan_prd_from_requirement(*, request, run_state, api_key, planner_memory=""):  # noqa: ANN001
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/memory-result",
                "description": "Write Ralph result to memory",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Create hello.txt",
                        "description": "Create hello.txt and complete the task",
                        "acceptanceCriteria": ["Task is completed"],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_get_bound_model(*, api_key, model, base_url, timeout):  # noqa: ANN001
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        (workspace / "hello.txt").write_text("hello\n", encoding="utf-8")
        payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        (run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        (run_dir / "progress.txt").write_text(
            "# Ralph Progress Log\nStarted: now\n---\n## 2026-03-12T19:10:00 - US-001\n- Created hello.txt.\n---\n",
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
            "session_id": session_id,
            "workspace_path": str(workspace),
            "requirement": "Create hello.txt for me.",
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert '"status": "complete"' in body

    session_root = Path(str(tmp_path / "pi-sessions"))
    session_files = list(session_root.rglob(f"*{session_id}.jsonl"))
    assert len(session_files) == 1

    reopened = SessionManager.open(session_files[0])
    context = reopened.build_session_context()
    rendered = [str(message.content) for message in context]

    assert "Create hello.txt for me." in rendered
    assert any("Ralph task result" in item for item in rendered)
    assert any("Status: complete" in item for item in rendered)
