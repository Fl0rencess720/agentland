from __future__ import annotations

"""统一 chat 服务：graph 路由 + pi 风格会话记忆 + SSE 输出。"""

import asyncio
import json
import os
import queue
import threading
import uuid
from collections.abc import Callable
from pathlib import Path
from typing import Literal

from fastapi import HTTPException
from fastapi.responses import StreamingResponse
from langchain_core.messages import AIMessage, AnyMessage, HumanMessage, SystemMessage, ToolMessage
from langchain_openai import ChatOpenAI

from app.models.session import SessionState
from app.schemas.chat import ChatStreamRequest
from app.schemas.ralph import RalphStreamRequest
from app.services.agent_loop import AgentConfig, Hooks, run_agent
from app.services.chat_router import route_prompt
from app.services.memory_compaction import (
    CompactionResult,
    compact_session,
    detect_context_overflow,
    maybe_auto_compact,
)
from app.services.ralph_service import run_ralph
from app.services.session_memory import SessionManager
from app.services.skills_service import inject_skills_into_messages
from app.services.tools import load_tools, tool_signature
_sessions: dict[str, SessionState] = {}
_sessions_lock = threading.Lock()
_model_cache: dict[tuple[str, str | None, float, tuple[str, ...]], object] = {}
_model_cache_lock = threading.Lock()


async def stream_chat(request: ChatStreamRequest) -> StreamingResponse:
    """执行一次统一 chat 请求并返回 SSE StreamingResponse。"""

    api_key = os.getenv("OPENAI_API_KEY")
    if not api_key:
        raise HTTPException(status_code=500, detail="OPENAI_API_KEY not set")

    session_id = request.session_id or f"session-{uuid.uuid4().hex[:8]}"
    async_queue: asyncio.Queue[dict[str, object] | None] = asyncio.Queue()
    loop = asyncio.get_running_loop()

    def emit(event: str, data: dict[str, object]) -> None:
        loop.call_soon_threadsafe(async_queue.put_nowait, {"event": event, "data": data})

    def worker() -> None:
        try:
            session = _get_or_create_session(
                session_id=session_id,
                workspace_path=request.workspace_path,
                system_prompt=request.system,
            )
            if request.deep:
                decision = route_prompt(
                    messages=[*session.manager.build_session_context(), HumanMessage(content=request.message)],
                    api_key=api_key,
                    model=request.model,
                    base_url=request.base_url,
                    timeout=request.timeout,
                )
            else:
                decision = "chat"
            emit("route", {"intent": decision})

            if decision == "task":
                _run_task_branch(request=request, session_id=session_id, emit=emit)
            else:
                _run_chat_branch(request=request, session_id=session_id, api_key=api_key, emit=emit)
        except HTTPException as exc:
            emit("error", {"status_code": exc.status_code, "message": exc.detail})
        except Exception as exc:  # noqa: BLE001
            emit("error", {"message": str(exc)})
        finally:
            loop.call_soon_threadsafe(async_queue.put_nowait, None)

    threading.Thread(target=worker, daemon=True).start()

    async def event_stream():
        while True:
            try:
                item = await asyncio.wait_for(async_queue.get(), timeout=15.0)
            except TimeoutError:
                yield _sse("ping", {"ts": int(asyncio.get_running_loop().time())})
                continue
            if item is None:
                break
            event = str(item["event"])
            data = item["data"]
            if isinstance(data, dict):
                yield _sse(event, data)

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
            "X-Accel-Buffering": "no",
        },
    )


def queue_steering(session_id: str, message: str) -> dict[str, object]:
    """向指定会话写入 steering 消息。"""

    session = _get_session_or_404(session_id)
    _queue_message(session.steering_queue, HumanMessage(content=message))
    return {"ok": True, "session_id": session.session_id}


def queue_followup(session_id: str, message: str) -> dict[str, object]:
    """向指定会话写入 follow-up 消息。"""

    session = _get_session_or_404(session_id)
    _queue_message(session.followup_queue, HumanMessage(content=message))
    return {"ok": True, "session_id": session.session_id}


def _run_chat_branch(
    *,
    request: ChatStreamRequest,
    session_id: str,
    api_key: str,
    emit: Callable[[str, dict[str, object]], None],
) -> None:
    session = _get_or_create_session(
        session_id=session_id,
        workspace_path=request.workspace_path,
        system_prompt=request.system,
    )

    with session.lock:
        if session.running:
            raise HTTPException(status_code=409, detail="session is already running")
        session.running = True

    try:
        emit(
            "session",
            {
                "session_id": session.session_id,
                "mode": "chat",
                "workspace_path": str(session.workspace_path),
                "session_file": str(session.manager.session_file),
            },
        )

        pre_result = maybe_auto_compact(
            manager=session.manager,
            model=request.model,
            api_key=api_key,
            base_url=request.base_url,
            timeout=request.timeout,
        )
        _emit_auto_compaction(emit=emit, result=pre_result, reason="threshold", will_retry=False)

        human_message = HumanMessage(content=request.message)
        session.manager.append_message(human_message)
        tools = load_tools()

        model = _call_get_bound_model(
            api_key=api_key,
            model=request.model,
            base_url=request.base_url,
            timeout=request.timeout,
            tools=tools,
        )
        stream_state = {"has_delta": False}

        def emit_assistant_delta(text: str) -> None:
            if not text:
                return
            stream_state["has_delta"] = True
            emit("assistant_delta", {"content": text})

        def run_once() -> list[AnyMessage]:
            local_history = inject_skills_into_messages(session.manager.build_session_context(), session.workspace_path)
            out = run_agent(
                local_history,
                AgentConfig(
                    model=model,
                    tools=tools,
                    max_turns=request.max_turns,
                    get_steering_messages=lambda: _drain_queue(session.steering_queue, request.queue_mode),
                    get_followup_messages=lambda: _drain_queue(session.followup_queue, request.queue_mode),
                    hooks=Hooks(
                        on_assistant_delta=emit_assistant_delta,
                        on_assistant=lambda message: _emit_assistant_fallback(emit_assistant_delta, message, stream_state),
                        on_tool_call=lambda call: emit(
                            "tool_call",
                            {
                                "id": call.get("id", ""),
                                "name": call.get("name", ""),
                                "args": call.get("args", {}),
                            },
                        ),
                        on_tool_result=lambda message: emit(
                            "tool_result",
                            {
                                "tool_call_id": message.tool_call_id or "",
                                "name": message.name or "",
                                "content": _normalize_tool_content(message.content),
                            },
                        ),
                    ),
                ),
            )
            for message in out[len(local_history) :]:
                session.manager.append_message(message)
            return out

        try:
            out = run_once()
        except Exception as exc:
            if not detect_context_overflow(exc):
                raise
            overflow_result = compact_session(
                manager=session.manager,
                model=request.model,
                api_key=api_key,
                base_url=request.base_url,
                timeout=request.timeout,
            )
            _emit_auto_compaction(emit=emit, result=overflow_result, reason="overflow", will_retry=True)
            if overflow_result is None:
                raise
            out = run_once()

        post_result = maybe_auto_compact(
            manager=session.manager,
            model=request.model,
            api_key=api_key,
            base_url=request.base_url,
            timeout=request.timeout,
        )
        _emit_auto_compaction(emit=emit, result=post_result, reason="threshold", will_retry=False)

        emit("done", {"session_id": session.session_id, "mode": "chat"})
    finally:
        with session.lock:
            session.running = False


def _run_task_branch(
    *,
    request: ChatStreamRequest,
    session_id: str,
    emit: Callable[[str, dict[str, object]], None],
) -> None:
    run_ralph(
        request=RalphStreamRequest(
            requirement=request.message,
            workspace_path=request.workspace_path,
            session_id=session_id,
            project_name=request.project_name,
            model=request.model,
            base_url=request.base_url,
            timeout=request.timeout,
            agent_max_turns=request.agent_max_turns,
            iterations=request.iterations,
        ),
        emit=emit,
    )


def _get_or_create_session(*, session_id: str, workspace_path: str | None, system_prompt: str) -> SessionState:
    resolved_workspace = Path(workspace_path or os.getcwd()).expanduser().resolve()

    with _sessions_lock:
        session = _sessions.get(session_id)
        if session is not None:
            if session.workspace_path != resolved_workspace:
                raise HTTPException(status_code=400, detail="session_id already exists for a different workspace")
            session.manager = SessionManager.open(session.manager.session_file)
            return session

        manager = SessionManager.open_or_create(
            cwd=resolved_workspace,
            session_id=session_id,
            system_prompt=system_prompt,
        )
        session = SessionState(
            session_id=session_id,
            workspace_path=resolved_workspace,
            manager=manager,
        )
        _sessions[session_id] = session
        return session


def _get_session_or_404(session_id: str) -> SessionState:
    with _sessions_lock:
        session = _sessions.get(session_id)
    if session is None:
        raise HTTPException(status_code=404, detail="session not found")
    return session


def _queue_message(q: queue.Queue[AnyMessage], message: AnyMessage) -> None:
    try:
        q.put_nowait(message)
    except queue.Full as exc:
        raise HTTPException(status_code=429, detail="queue is full") from exc


def _drain_queue(q: queue.Queue[AnyMessage], mode: Literal["one-at-a-time", "all"]) -> list[AnyMessage]:
    if mode == "all":
        out: list[AnyMessage] = []
        while True:
            try:
                out.append(q.get_nowait())
            except queue.Empty:
                return out

    try:
        return [q.get_nowait()]
    except queue.Empty:
        return []


def _get_bound_model(*, api_key: str, model: str, base_url: str | None, timeout: float, tools: list[object]):
    key = (model, base_url, timeout, tool_signature(tools))
    with _model_cache_lock:
        bound = _model_cache.get(key)
        if bound is not None:
            return bound

        llm = ChatOpenAI(
            model=model,
            api_key=api_key,
            base_url=base_url,
            streaming=True,
            timeout=timeout,
            max_retries=1,
            stream_usage=False,
            use_responses_api=False,
        )
        bound = llm.bind_tools(tools)
        _model_cache[key] = bound
        return bound


def _call_get_bound_model(
    *,
    api_key: str,
    model: str,
    base_url: str | None,
    timeout: float,
    tools: list[object],
):
    """兼容旧测试桩：tools 参数不可用时回退到旧签名。"""

    try:
        return _get_bound_model(
            api_key=api_key,
            model=model,
            base_url=base_url,
            timeout=timeout,
            tools=tools,
        )
    except TypeError as exc:
        if "tools" not in str(exc):
            raise
        return _get_bound_model(
            api_key=api_key,
            model=model,
            base_url=base_url,
            timeout=timeout,
        )


def _emit_assistant_fallback(
    emit_assistant_delta: Callable[[str], None],
    message: AIMessage,
    stream_state: dict[str, bool],
) -> None:
    if stream_state.get("has_delta", False):
        return

    content = _extract_text_content(message.content)
    if content:
        emit_assistant_delta(content)
        return

    refusal = message.additional_kwargs.get("refusal")
    if isinstance(refusal, str) and refusal:
        emit_assistant_delta(refusal)


def _extract_text_content(content: object) -> str:
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""

    parts: list[str] = []
    for item in content:
        if isinstance(item, str):
            parts.append(item)
            continue
        if isinstance(item, dict):
            text = item.get("text")
            if isinstance(text, str):
                parts.append(text)
                continue
            nested = item.get("content")
            if isinstance(nested, str):
                parts.append(nested)
                continue
        text_attr = getattr(item, "text", None)
        if isinstance(text_attr, str):
            parts.append(text_attr)
            continue
        content_attr = getattr(item, "content", None)
        if isinstance(content_attr, str):
            parts.append(content_attr)

    return "".join(parts).strip()


def _normalize_tool_content(content: object) -> object:
    if isinstance(content, list):
        parts: list[object] = []
        for item in content:
            if isinstance(item, dict):
                parts.append(item)
            else:
                parts.append({"type": "text", "text": str(item)})
        return parts
    return content


def _emit_auto_compaction(
    *,
    emit: Callable[[str, dict[str, object]], None],
    result: CompactionResult | None,
    reason: Literal["threshold", "overflow"],
    will_retry: bool,
) -> None:
    if result is None:
        return
    emit("auto_compaction_start", {"reason": reason})
    emit(
        "auto_compaction_end",
        {
            "reason": reason,
            "aborted": False,
            "will_retry": will_retry,
            "result": {
                "summary": result.summary,
                "first_kept_entry_id": result.first_kept_entry_id,
                "tokens_before": result.tokens_before,
                "details": result.details,
            },
        },
    )


def _sse(event: str, data: dict[str, object]) -> str:
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"
