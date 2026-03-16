from __future__ import annotations

import json
from pathlib import Path

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

pytest.importorskip("langgraph")

from app.schemas.chat import ChatStreamRequest
from app.services.memory_compaction import (
    CompactionSettings,
    compact_session,
    prepare_compaction,
    serialize_conversation,
)
from app.services.chat_service import _run_chat_branch
from app.services.session_memory import SessionManager


def test_compaction_rebuilds_context_from_summary(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="compact-replay",
        system_prompt="sys",
    )
    manager.append_message(HumanMessage(content="first"))
    manager.append_message(AIMessage(content="reply-1"))
    manager.append_message(HumanMessage(content="second"))
    manager.append_message(AIMessage(content="reply-2"))

    monkeypatch.setattr(
        "app.services.memory_compaction._generate_summary_text",
        lambda **kwargs: "## Goal\nSummarized old work.",
    )
    result = compact_session(
        manager=manager,
        model="gpt-5.2-codex",
        api_key="test-key",
        base_url=None,
        timeout=30.0,
        settings=CompactionSettings(enabled=True, reserve_tokens=16, keep_recent_tokens=3),
    )

    assert result is not None
    context = manager.build_session_context()
    rendered = [str(message.content) for message in context]
    assert rendered == [
        "sys",
        "Compaction summary:\n## Goal\nSummarized old work.",
        "second",
        "reply-2",
    ]

    reopened = SessionManager.open(manager.session_file)
    entry_types = [str(entry["type"]) for entry in reopened.get_entries()]
    assert "compaction" in entry_types


def test_prepare_compaction_marks_split_turn(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="split-turn",
        system_prompt="sys",
    )
    manager.append_message(HumanMessage(content="implement feature"))
    manager.append_message(
        AIMessage(
            content="Reading project files.",
            tool_calls=[{"id": "call-1", "name": "read", "args": {"path": str(workspace / "README.md")}}],
        )
    )
    manager.append_message(
        ToolMessage(
            content="A" * 64,
            tool_call_id="call-1",
            name="read",
        )
    )
    manager.append_message(AIMessage(content="Applying the change now."))

    preparation = prepare_compaction(
        manager.get_branch(),
        CompactionSettings(enabled=True, reserve_tokens=16, keep_recent_tokens=1),
    )

    assert preparation is not None
    assert preparation.is_split_turn is True
    assert [type(message) for message in preparation.turn_prefix_messages] == [
        HumanMessage,
        AIMessage,
        ToolMessage,
    ]
    assert preparation.messages_to_summarize == []


def test_compaction_tracks_files_and_truncates_tool_results(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    target = workspace / "hello.txt"
    manager = SessionManager.open_or_create(
        cwd=workspace,
        session_id="file-tracking",
        system_prompt="sys",
    )
    manager.append_message(HumanMessage(content="read the file"))
    manager.append_message(
        AIMessage(
            content="I will inspect it.",
            tool_calls=[{"id": "call-1", "name": "read", "args": {"path": str(target)}}],
        )
    )
    manager.append_message(
        ToolMessage(
            content="B" * 2505,
            tool_call_id="call-1",
            name="read",
        )
    )
    manager.append_message(HumanMessage(content="done"))
    manager.append_message(AIMessage(content="done"))

    serialization = serialize_conversation(
        [
            AIMessage(
                content="I will inspect it.",
                tool_calls=[{"id": "call-1", "name": "read", "args": {"path": str(target)}}],
            ),
            ToolMessage(content="B" * 2505, tool_call_id="call-1", name="read"),
        ]
    )
    assert "[Assistant tool calls]: read(" in serialization
    assert "more characters truncated" in serialization

    monkeypatch.setattr(
        "app.services.memory_compaction._generate_summary_text",
        lambda **kwargs: "## Goal\nTrack read files.",
    )
    result = compact_session(
        manager=manager,
        model="gpt-5.2-codex",
        api_key="test-key",
        base_url=None,
        timeout=30.0,
        settings=CompactionSettings(enabled=True, reserve_tokens=16, keep_recent_tokens=3),
    )

    assert result is not None
    assert result.details["readFiles"] == [str(target)]
    assert result.details["modifiedFiles"] == []


def test_chat_branch_emits_auto_compaction_events(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setenv("OPENAI_API_KEY", "test-key")
    monkeypatch.setenv("PI_SESSION_ROOT", str(tmp_path / "pi-sessions"))
    monkeypatch.setenv("AGENTLAND_CONTEXT_WINDOW", "12")
    monkeypatch.setenv("AGENTLAND_COMPACTION_RESERVE_TOKENS", "2")
    monkeypatch.setenv("AGENTLAND_COMPACTION_KEEP_RECENT_TOKENS", "1")

    workspace = tmp_path / "workspace"
    workspace.mkdir()

    def fake_get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float) -> object:
        return object()

    def fake_run_agent(messages, cfg):  # noqa: ANN001
        reply = AIMessage(
            content="This answer is long enough to trigger compaction.",
            usage_metadata={"input_tokens": 8, "output_tokens": 8, "total_tokens": 16},
        )
        if cfg.hooks.on_assistant is not None:
            cfg.hooks.on_assistant(reply)
        return [*messages, reply]

    monkeypatch.setattr("app.services.chat_service._get_bound_model", fake_get_bound_model)
    monkeypatch.setattr("app.services.chat_service.run_agent", fake_run_agent)
    monkeypatch.setattr(
        "app.services.memory_compaction._generate_summary_text",
        lambda **kwargs: "## Goal\nAuto compact chat memory.",
    )

    events: list[tuple[str, dict[str, object]]] = []
    _run_chat_branch(
        request=ChatStreamRequest(
            session_id="chat-auto-compact",
            workspace_path=str(workspace),
            message="hello",
        ),
        session_id="chat-auto-compact",
        api_key="test-key",
        emit=lambda event, data: events.append((event, data)),
    )

    assert any(event == "auto_compaction_start" for event, _ in events)
    assert any(event == "auto_compaction_end" for event, _ in events)

    session_root = tmp_path / "pi-sessions"
    session_files = list(session_root.rglob("*chat-auto-compact.jsonl"))
    assert len(session_files) == 1
    entries = [json.loads(line) for line in session_files[0].read_text(encoding="utf-8").splitlines()]
    assert any(entry.get("type") == "compaction" for entry in entries[1:])

    reopened = SessionManager.open(session_files[0])
    rendered = [str(message.content) for message in reopened.build_session_context()]
    assert any("Compaction summary:" in item and "Auto compact chat memory." in item for item in rendered)
