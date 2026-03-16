from __future__ import annotations

"""Unified chat graph and persistent memory tests."""

import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage

pytest.importorskip("langgraph")

from app.main import app
from app.models.ralph import RalphPrd
from app.services.chat_router import _invoke_router_model
from app.services.session_memory import SessionManager


def _run_dir(workspace: Path, session_id: str, run_id: str = "run-0001") -> Path:
    return workspace / ".ralph" / session_id / run_id


def test_session_manager_persists_and_rebuilds_context(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """SessionManager should rebuild persisted message context from JSONL."""

    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))
    workspace = tmp_path / "workspace"
    workspace.mkdir()

    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="session-memory",
        system_prompt="You are persistent.",
    )
    manager.append_message(HumanMessage(content="hello"))
    manager.append_message(AIMessage(content="world"))

    reopened = SessionManager.open(manager.session_file)
    context = reopened.build_session_context()

    assert [type(message) for message in context] == [SystemMessage, HumanMessage, AIMessage]
    assert str(context[0].content) == "You are persistent."
    assert str(context[1].content) == "hello"
    assert str(context[2].content) == "world"


def test_session_manager_supports_branch_summaries(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Branch summaries should preserve abandoned-branch context on the new leaf."""

    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))
    workspace = tmp_path / "workspace"
    workspace.mkdir()

    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="session-branch",
        system_prompt="sys",
    )
    manager.append_message(HumanMessage(content="first"))
    first_reply_id = manager.append_message(AIMessage(content="reply-1"))
    manager.append_message(HumanMessage(content="second"))
    manager.append_message(AIMessage(content="reply-2"))
    manager.branch_with_summary(entry_id=first_reply_id, summary="The abandoned branch already handled the second turn.")
    manager.append_message(HumanMessage(content="retry"))

    reopened = SessionManager.open(manager.session_file)
    context = reopened.build_session_context()
    rendered = [str(message.content) for message in context]

    assert rendered == [
        "sys",
        "first",
        "reply-1",
        "Branch summary:\nThe abandoned branch already handled the second turn.",
        "retry",
    ]


def test_chat_stream_routes_to_chat_branch(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Unified chat endpoint should route normal chat prompts to the chat agent."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    client = TestClient(app)

    def fake_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        reply = AIMessage(content="你好，我在。")
        if cfg.hooks.on_assistant is not None:
            cfg.hooks.on_assistant(reply)
        return [*messages, reply]

    monkeypatch.setattr(
        "app.services.chat_service.route_prompt",
        lambda **_: pytest.fail("route_prompt should not be called when deep=false"),
    )
    monkeypatch.setattr("app.services.chat_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.chat_service.run_agent", fake_run_agent)

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": "chat-route",
            "workspace_path": str(workspace),
            "message": "你好",
            "deep": False,
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert 'event: route' in body
    assert '"intent": "chat"' in body
    assert 'event: session' in body
    assert 'event: assistant_delta' in body
    assert '"mode": "chat"' in body

    session_root = Path(str(tmp_path / "pi-sessions"))
    session_files = list(session_root.rglob("*.jsonl"))
    assert len(session_files) == 1
    entries = [json.loads(line) for line in session_files[0].read_text(encoding="utf-8").splitlines()]
    assert entries[0]["type"] == "session"
    assert entries[1]["message"]["role"] == "system"
    assert entries[2]["message"]["role"] == "user"
    assert entries[2]["message"]["content"] == "你好"
    assert entries[3]["message"]["role"] == "assistant"


def test_chat_stream_routes_to_task_branch(monkeypatch: pytest.MonkeyPatch) -> None:
    """Unified chat endpoint should route task prompts to Ralph."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    client = TestClient(app)

    def fake_route_prompt(*, messages, api_key: str, model: str, base_url: str | None, timeout: float) -> str:  # noqa: ANN001
        assert "创建文件" in str(messages[-1].content)
        return "task"

    def fake_run_ralph(*, request, emit):  # noqa: ANN001
        emit("session", {"session_id": request.session_id, "workspace_path": request.workspace_path})
        emit("done", {"session_id": request.session_id, "status": "complete", "iteration": 1})

    monkeypatch.setattr("app.services.chat_service.route_prompt", fake_route_prompt)
    monkeypatch.setattr("app.services.chat_service.run_ralph", fake_run_ralph)

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": "task-route",
            "workspace_path": "/tmp/task-route",
            "message": "请创建文件并记录进度",
            "deep": True,
        },
    ) as response:
        body = "".join(response.iter_text())

    assert response.status_code == 200
    assert 'event: route' in body
    assert '"intent": "task"' in body
    assert 'event: session' in body
    assert '"status": "complete"' in body


def test_router_model_uses_streaming_json(monkeypatch: pytest.MonkeyPatch) -> None:
    """Router model path should use stream=true and parse JSON output."""

    captured: dict[str, object] = {}

    class FakeChunk:
        def __init__(self, content: str) -> None:
            self.content = content

        def __add__(self, other: "FakeChunk") -> "FakeChunk":
            return FakeChunk(self.content + other.content)

    class FakeRouter:
        def __init__(self, **kwargs: object) -> None:
            captured["kwargs"] = kwargs

        def stream(self, messages: list[object]) -> list[FakeChunk]:
            captured["messages"] = messages
            return [
                FakeChunk('{"intent":"task",'),
                FakeChunk('"reason":"needs workspace changes"}'),
            ]

    monkeypatch.setattr("app.services.chat_router.ChatOpenAI", FakeRouter)
    decision = _invoke_router_model(
        messages=[HumanMessage(content="请创建一个文件")],
        api_key="test-key",
        model="gpt-5.2-codex",
        base_url="https://example.com/v1",
        timeout=30.0,
    )

    assert decision == "task"
    assert captured["kwargs"]["streaming"] is True
    assert captured["kwargs"]["use_responses_api"] is False


def test_chat_to_ralph_to_chat_reuses_memory(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Prior chat memory should inform Ralph planning, and Ralph results should return to chat memory."""

    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    client = TestClient(app)
    session_id = "memory-handoff"
    run_dir = _run_dir(workspace, session_id)
    chat_turn = {"count": 0}
    captured: dict[str, str] = {}
    third_chat_context: dict[str, list[str]] = {}

    def fake_route_prompt(*, messages, api_key: str, model: str, base_url: str | None, timeout: float) -> str:  # noqa: ANN001
        latest = str(messages[-1].content)
        if "创建" in latest or "create" in latest.lower():
            return "task"
        return "chat"

    def fake_chat_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_ralph_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_chat_run_agent(messages, cfg):  # noqa: ANN001
        chat_turn["count"] += 1
        if chat_turn["count"] == 1:
            reply = AIMessage(content="记住：我偏好 TypeScript。")
        else:
            rendered = [str(message.content) for message in messages if isinstance(message, (SystemMessage, HumanMessage, AIMessage))]
            third_chat_context["messages"] = rendered
            reply = AIMessage(content="我记得你偏好 TypeScript，而且 Ralph 已完成 hello.txt。")
        if cfg.hooks.on_assistant is not None:
            cfg.hooks.on_assistant(reply)
        return [*messages, reply]

    def fake_plan_prd_from_requirement(*, request, run_state, api_key, planner_memory=""):  # noqa: ANN001
        captured["planner_memory"] = planner_memory
        return RalphPrd.model_validate(
            {
                "project": "demo",
                "branchName": "ralph/hello-task",
                "description": "Create hello.txt",
                "userStories": [
                    {
                        "id": "US-001",
                        "title": "Create hello.txt",
                        "description": "Create hello.txt in the workspace root",
                        "acceptanceCriteria": [
                            "A file named hello.txt exists at the repository root",
                            'The file content equals exactly: "hello ralph"',
                        ],
                        "priority": 1,
                        "passes": False,
                        "notes": "",
                    }
                ],
            }
        )

    def fake_ralph_run_agent(messages, cfg):  # noqa: ANN001
        (workspace / "hello.txt").write_text("hello ralph\n", encoding="utf-8")
        payload = json.loads((run_dir / "prd.json").read_text(encoding="utf-8"))
        payload["userStories"][0]["passes"] = True
        (run_dir / "prd.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        (run_dir / "progress.txt").write_text(
            "# Ralph Progress Log\n"
            "Started: now\n"
            "---\n"
            "## 2026-03-12T20:00:00 - US-001\n"
            "- Created hello.txt with content hello ralph.\n"
            "---\n",
            encoding="utf-8",
        )
        return [*messages, AIMessage(content="<promise>COMPLETE</promise>")]

    monkeypatch.setattr("app.services.chat_service.route_prompt", fake_route_prompt)
    monkeypatch.setattr("app.services.chat_service._get_bound_model", fake_chat_get_bound_model)
    monkeypatch.setattr("app.services.chat_service.run_agent", fake_chat_run_agent)
    monkeypatch.setattr("app.services.ralph_service._get_bound_model", fake_ralph_get_bound_model)
    monkeypatch.setattr("app.services.ralph_service._plan_prd_from_requirement", fake_plan_prd_from_requirement)
    monkeypatch.setattr("app.services.ralph_service.run_agent", fake_ralph_run_agent)

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(workspace),
            "message": "记住我的偏好是 TypeScript",
            "deep": False,
        },
    ) as response:
        first_body = "".join(response.iter_text())

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(workspace),
            "message": "请创建 hello.txt，内容是 hello ralph",
            "deep": True,
        },
    ) as response:
        second_body = "".join(response.iter_text())

    with client.stream(
        "POST",
        "/v1/chat/stream",
        json={
            "session_id": session_id,
            "workspace_path": str(workspace),
            "message": "你还记得刚才做了什么吗？",
            "deep": False,
        },
    ) as response:
        third_body = "".join(response.iter_text())

    assert "记住：我偏好 TypeScript。" in first_body
    assert '"intent": "task"' in second_body
    assert '"status": "complete"' in second_body
    assert "我偏好 TypeScript" in captured["planner_memory"]
    assert "messages" in third_chat_context
    assert any("TypeScript" in item for item in third_chat_context["messages"])
    assert any("Ralph task result" in item for item in third_chat_context["messages"])
    assert any("hello.txt" in item for item in third_chat_context["messages"])
    assert "我记得你偏好 TypeScript，而且 Ralph 已完成 hello.txt。" in third_body

    session_root = Path(str(tmp_path / "pi-sessions"))
    session_files = list(session_root.rglob(f"*{session_id}.jsonl"))
    assert len(session_files) == 1
    reopened = SessionManager.open(session_files[0])
    rendered = [str(message.content) for message in reopened.build_session_context()]
    assert any("Ralph task result" in item for item in rendered)
