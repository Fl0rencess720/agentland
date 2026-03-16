from __future__ import annotations

"""Ralph-style orchestration built on top of the existing agent loop."""

import asyncio
import json
import os
import re
import threading
import uuid
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

from fastapi import HTTPException
from fastapi.responses import StreamingResponse
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langchain_openai import ChatOpenAI

from app.models.ralph import RalphPrd, RalphRunLock, RalphRunState
from app.schemas.chat import DEFAULT_SYSTEM_PROMPT
from app.schemas.ralph import RalphStreamRequest
from app.services.agent_loop import AgentConfig, Hooks, run_agent
from app.services.session_memory import SessionManager
from app.services.skills_service import build_skill_aware_system_prompt
from app.services.tools import load_tools, tool_signature
_run_locks: dict[str, RalphRunLock] = {}
_run_locks_guard = threading.Lock()
_model_cache: dict[tuple[str, str | None, float, tuple[str, ...]], object] = {}
_model_cache_lock = threading.Lock()

_COMPLETE_MARKER = "<promise>COMPLETE</promise>"
type EventEmitter = Callable[[str, dict[str, object]], None]


@dataclass(slots=True)
class _PlanningResult:
    """Initial planning output for a Ralph run."""

    prd: RalphPrd
    fallback_reason: str | None = None


@dataclass(slots=True)
class _WorkspaceSnapshot:
    """Workspace state captured around a Ralph iteration."""

    progress_text: str
    file_signatures: dict[str, tuple[int, int]]


@dataclass(slots=True, frozen=True)
class _RunCandidate:
    """An existing Ralph run that can be resumed or skipped."""

    run_id: str
    run_dir: Path
    prd_path: Path
    progress_path: Path
    order: int
    complete: bool


async def stream_ralph(request: RalphStreamRequest) -> StreamingResponse:
    """Run a Ralph-compatible outer loop and stream lifecycle events."""

    async_queue: asyncio.Queue[dict[str, object] | None] = asyncio.Queue()
    loop = asyncio.get_running_loop()

    def emit(event: str, data: dict[str, object]) -> None:
        loop.call_soon_threadsafe(async_queue.put_nowait, {"event": event, "data": data})

    def worker() -> None:
        try:
            run_ralph(request=request, emit=emit)
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
                yield _sse("ping", {"ts": int(asyncio.get_event_loop().time())})
                continue
            if item is None:
                break
            yield _sse(str(item["event"]), item["data"])

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
            "X-Accel-Buffering": "no",
        },
    )


def run_ralph(*, request: RalphStreamRequest, emit: EventEmitter) -> None:
    """同步执行 Ralph loop，并通过 emitter 输出事件。"""

    api_key = os.getenv("OPENAI_API_KEY")
    if not api_key:
        raise HTTPException(status_code=500, detail="OPENAI_API_KEY not set")
    if request.iterations <= 0:
        raise HTTPException(status_code=400, detail="iterations must be > 0")
    if request.agent_max_turns <= 0:
        raise HTTPException(status_code=400, detail="agent_max_turns must be > 0")

    session_id = request.session_id or f"ralph-{uuid.uuid4().hex[:8]}"
    run_lock = _get_or_create_run_lock(session_id)

    with run_lock.lock:
        if run_lock.running:
            raise HTTPException(status_code=409, detail="ralph session is already running")
        run_lock.running = True

    try:
        run_state = _build_run_state(session_id=session_id, workspace_path=request.workspace_path)
        session_manager = SessionManager.open_or_create(
            cwd=run_state.workspace_path,
            session_id=session_id,
            system_prompt=DEFAULT_SYSTEM_PROMPT,
        )
        planner_memory = _render_memory_for_planner(session_manager.build_session_context())
        is_new_run = not run_state.prd_path.exists()
        if is_new_run:
            session_manager.append_message(HumanMessage(content=request.requirement))
        planning = _ensure_run_files(
            request=request,
            run_state=run_state,
            api_key=api_key,
            planner_memory=planner_memory,
        )
        prd = planning.prd
        emit(
            "session",
            {
                "session_id": session_id,
                "workspace_path": str(run_state.workspace_path),
                "session_root": str(run_state.session_root),
                "run_id": run_state.run_id,
                "run_dir": str(run_state.run_dir),
                "prd_path": str(run_state.prd_path),
                "progress_path": str(run_state.progress_path),
            },
        )
        if planning.fallback_reason is not None:
            emit(
                "planner_fallback",
                {
                    "reason": planning.fallback_reason,
                    "mode": "single_story_fallback",
                },
            )
        emit(
            "plan_ready",
            {
                "branch_name": prd.branchName,
                "stories": [
                    {
                        "id": story.id,
                        "title": story.title,
                        "priority": story.priority,
                        "passes": story.passes,
                    }
                    for story in prd.sorted_stories()
                ],
            },
        )
        tools = load_tools()

        model = _call_get_bound_model(
            api_key=api_key,
            model=request.model,
            base_url=request.base_url,
            timeout=request.timeout,
            tools=tools,
        )

        for iteration in range(1, request.iterations + 1):
            prd_before_iteration = _read_prd(run_state.prd_path)
            workspace_before_iteration = _snapshot_workspace(run_state)
            emit(
                "iteration_start",
                {
                    "iteration": iteration,
                    "max_iterations": request.iterations,
                },
            )
            stream_state = {"has_delta": False}

            def emit_assistant_delta(text: str) -> None:
                if not text:
                    return
                stream_state["has_delta"] = True
                emit(
                    "assistant_delta",
                    {
                        "iteration": iteration,
                        "content": text,
                    },
                )

            history = [
                SystemMessage(content=build_skill_aware_system_prompt(_build_iteration_system_prompt(run_state), run_state.workspace_path)),
                HumanMessage(
                    content=_build_iteration_user_prompt(
                        request=request,
                        run_state=run_state,
                        iteration=iteration,
                        max_iterations=request.iterations,
                    )
                ),
            ]
            out = run_agent(
                history,
                AgentConfig(
                    model=model,
                    tools=tools,
                    max_turns=request.agent_max_turns,
                    hooks=Hooks(
                        on_assistant_delta=emit_assistant_delta,
                        on_assistant=lambda message: _emit_assistant_fallback(
                            emit_assistant_delta,
                            message,
                            stream_state,
                        ),
                        on_tool_call=lambda call: emit(
                            "tool_call",
                            {
                                "iteration": iteration,
                                "id": call.get("id", ""),
                                "name": call.get("name", ""),
                                "args": call.get("args", {}),
                            },
                        ),
                        on_tool_result=lambda message: emit(
                            "tool_result",
                            {
                                "iteration": iteration,
                                "tool_call_id": message.tool_call_id or "",
                                "name": message.name or "",
                                "content": _normalize_tool_content(message.content),
                            },
                        ),
                    ),
                ),
            )

            final_message = _find_last_ai_message(out)
            final_text = _extract_text_content(final_message.content) if final_message is not None else ""
            prd = _reconcile_prd_with_workspace(
                run_state=run_state,
                previous_snapshot=workspace_before_iteration,
            )
            run_complete = all(story.passes for story in prd.userStories)
            emit(
                "iteration_complete",
                {
                    "iteration": iteration,
                    "complete": run_complete,
                },
            )
            if run_complete:
                emit(
                    "done",
                    {
                        "session_id": session_id,
                        "status": "complete",
                        "iteration": iteration,
                    },
                )
                _append_ralph_result_to_memory(
                    session_manager=session_manager,
                    request=request,
                    run_state=run_state,
                    status="complete",
                    iteration=iteration,
                )
                return
            if _COMPLETE_MARKER in final_text and any(not story.passes for story in prd_before_iteration.userStories):
                emit(
                    "planner_fallback",
                    {
                        "reason": "agent emitted COMPLETE before all stories passed; continuing iterations",
                        "mode": "completion_guard",
                    },
                )

        emit(
            "done",
            {
                "session_id": session_id,
                "status": "max_iterations_reached",
                "iteration": request.iterations,
            },
        )
        _append_ralph_result_to_memory(
            session_manager=session_manager,
            request=request,
            run_state=run_state,
            status="max_iterations_reached",
            iteration=request.iterations,
        )
    finally:
        with run_lock.lock:
            run_lock.running = False


def _get_or_create_run_lock(session_id: str) -> RalphRunLock:
    with _run_locks_guard:
        run_lock = _run_locks.get(session_id)
        if run_lock is None:
            run_lock = RalphRunLock(session_id)
            _run_locks[session_id] = run_lock
        return run_lock


def _build_run_state(session_id: str, workspace_path: str | None) -> RalphRunState:
    workspace = _resolve_workspace_path(workspace_path)
    session_root = workspace / ".ralph" / session_id
    candidate = _select_resumable_run(session_root)
    if candidate is not None:
        run_id = candidate.run_id
        run_dir = candidate.run_dir
    else:
        run_id = _next_run_id(session_root)
        run_dir = session_root / run_id

    return RalphRunState(
        session_id=session_id,
        run_id=run_id,
        workspace_path=workspace,
        session_root=session_root,
        run_dir=run_dir,
        prd_path=run_dir / "prd.json",
        progress_path=run_dir / "progress.txt",
    )


def _resolve_workspace_path(workspace_path: str | None) -> Path:
    workspace = Path(workspace_path or os.getcwd()).expanduser().resolve()
    if not workspace.exists():
        raise HTTPException(status_code=400, detail=f"workspace does not exist: {workspace}")
    if not workspace.is_dir():
        raise HTTPException(status_code=400, detail=f"workspace is not a directory: {workspace}")
    return workspace


def _select_resumable_run(session_root: Path) -> _RunCandidate | None:
    candidates = _list_run_candidates(session_root)
    unfinished = [candidate for candidate in candidates if not candidate.complete]
    if unfinished:
        return unfinished[-1]
    return None


def _next_run_id(session_root: Path) -> str:
    next_index = 1
    for candidate in _list_run_candidates(session_root):
        if candidate.run_id == "legacy":
            continue
        try:
            next_index = max(next_index, int(candidate.run_id.removeprefix("run-")) + 1)
        except ValueError:
            continue
    return f"run-{next_index:04d}"


def _list_run_candidates(session_root: Path) -> list[_RunCandidate]:
    candidates: list[_RunCandidate] = []
    legacy_candidate = _candidate_from_run_dir(run_dir=session_root, run_id="legacy", order=0)
    if legacy_candidate is not None:
        candidates.append(legacy_candidate)

    if not session_root.exists():
        return candidates

    for run_dir in sorted(
        (
            path
            for path in session_root.iterdir()
            if path.is_dir() and re.fullmatch(r"run-\d{4}", path.name)
        ),
        key=lambda path: path.name,
    ):
        run_id = run_dir.name
        try:
            order = int(run_id.removeprefix("run-"))
        except ValueError:
            continue
        candidate = _candidate_from_run_dir(run_dir=run_dir, run_id=run_id, order=order)
        if candidate is not None:
            candidates.append(candidate)
    return sorted(candidates, key=lambda candidate: candidate.order)


def _candidate_from_run_dir(*, run_dir: Path, run_id: str, order: int) -> _RunCandidate | None:
    prd_path = run_dir / "prd.json"
    if not prd_path.exists():
        return None

    try:
        prd = _read_prd(prd_path)
    except Exception:  # noqa: BLE001
        return None

    return _RunCandidate(
        run_id=run_id,
        run_dir=run_dir,
        prd_path=prd_path,
        progress_path=run_dir / "progress.txt",
        order=order,
        complete=all(story.passes for story in prd.userStories),
    )


def _ensure_run_files(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    api_key: str,
    planner_memory: str,
) -> _PlanningResult:
    run_state.run_dir.mkdir(parents=True, exist_ok=True)
    if run_state.prd_path.exists():
        return _PlanningResult(
            prd=RalphPrd.model_validate_json(run_state.prd_path.read_text(encoding="utf-8"))
        )

    planning = _coerce_planning_result(
        _call_plan_prd_from_requirement(
            request=request,
            run_state=run_state,
            api_key=api_key,
            planner_memory=planner_memory,
        )
    )
    run_state.prd_path.write_text(
        json.dumps(planning.prd.model_dump(mode="json"), ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    if not run_state.progress_path.exists():
        run_state.progress_path.write_text(
            "# Ralph Progress Log\n"
            f"Started: {datetime.now().isoformat(timespec='seconds')}\n"
            "---\n",
            encoding="utf-8",
        )
    return planning


def _read_prd(prd_path: Path) -> RalphPrd:
    return RalphPrd.model_validate_json(prd_path.read_text(encoding="utf-8"))


def _write_prd(prd_path: Path, prd: RalphPrd) -> None:
    prd_path.write_text(
        json.dumps(prd.model_dump(mode="json"), ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def _coerce_planning_result(result: _PlanningResult | RalphPrd) -> _PlanningResult:
    """兼容旧测试桩：允许直接返回 RalphPrd。"""

    if isinstance(result, _PlanningResult):
        return result
    if isinstance(result, RalphPrd):
        return _PlanningResult(prd=result)
    raise TypeError(f"unexpected planning result: {type(result)!r}")


def _snapshot_workspace(run_state: RalphRunState) -> _WorkspaceSnapshot:
    progress_text = ""
    if run_state.progress_path.exists():
        progress_text = run_state.progress_path.read_text(encoding="utf-8")

    file_signatures: dict[str, tuple[int, int]] = {}
    for file_path in run_state.workspace_path.rglob("*"):
        if not file_path.is_file():
            continue
        relative_path = file_path.relative_to(run_state.workspace_path)
        if _should_ignore_snapshot_path(relative_path):
            continue
        stat = file_path.stat()
        file_signatures[relative_path.as_posix()] = (stat.st_size, stat.st_mtime_ns)

    return _WorkspaceSnapshot(progress_text=progress_text, file_signatures=file_signatures)


def _should_ignore_snapshot_path(relative_path: Path) -> bool:
    parts = relative_path.parts
    return bool(parts) and parts[0] in {".git", ".ralph"}


def _reconcile_prd_with_workspace(
    *,
    run_state: RalphRunState,
    previous_snapshot: _WorkspaceSnapshot,
) -> RalphPrd:
    prd = _read_prd(run_state.prd_path)
    current_snapshot = _snapshot_workspace(run_state)
    changed_paths = _changed_workspace_paths(previous_snapshot, current_snapshot)

    updated = False
    for story in prd.userStories:
        if story.passes:
            continue
        if _story_matches_workspace(
            story=story,
            run_state=run_state,
            previous_snapshot=previous_snapshot,
            current_snapshot=current_snapshot,
            changed_paths=changed_paths,
        ):
            story.passes = True
            updated = True

    if updated:
        _write_prd(run_state.prd_path, prd)
    return prd


def _changed_workspace_paths(
    previous_snapshot: _WorkspaceSnapshot,
    current_snapshot: _WorkspaceSnapshot,
) -> set[str]:
    changed: set[str] = set()
    all_paths = set(previous_snapshot.file_signatures) | set(current_snapshot.file_signatures)
    for path in all_paths:
        if previous_snapshot.file_signatures.get(path) != current_snapshot.file_signatures.get(path):
            changed.add(path)
    return changed


def _story_matches_workspace(
    *,
    story: object,
    run_state: RalphRunState,
    previous_snapshot: _WorkspaceSnapshot,
    current_snapshot: _WorkspaceSnapshot,
    changed_paths: set[str],
) -> bool:
    title = getattr(story, "title", "")
    description = getattr(story, "description", "")
    acceptance = getattr(story, "acceptanceCriteria", [])
    story_text = "\n".join([str(title), str(description), *(str(item) for item in acceptance)])
    story_text_lower = story_text.lower()

    criteria = [str(item) for item in acceptance]
    if criteria and all(
        _criterion_matches_workspace(
            criterion=criterion,
            story_text=story_text,
            story_text_lower=story_text_lower,
            run_state=run_state,
            previous_snapshot=previous_snapshot,
            current_snapshot=current_snapshot,
            changed_paths=changed_paths,
        )
        for criterion in criteria
    ):
        return True

    if _mentions_progress_log(story_text_lower) and _progress_log_was_updated(previous_snapshot, current_snapshot):
        return True

    skill_names = _extract_skill_names(story_text_lower)
    if skill_names and any(_skill_artifact_exists(run_state, skill_name, changed_paths) for skill_name in skill_names):
        return True

    file_names = _extract_file_names(story_text)
    if file_names and all((run_state.workspace_path / file_name).is_file() for file_name in file_names):
        return True

    return False


def _criterion_matches_workspace(
    *,
    criterion: str,
    story_text: str,
    story_text_lower: str,
    run_state: RalphRunState,
    previous_snapshot: _WorkspaceSnapshot,
    current_snapshot: _WorkspaceSnapshot,
    changed_paths: set[str],
) -> bool:
    criterion_lower = criterion.lower()
    file_names = _extract_file_names(f"{story_text}\n{criterion}")

    if "file named" in criterion_lower and file_names:
        return all((run_state.workspace_path / file_name).is_file() for file_name in file_names)

    if "content equals exactly:" in criterion_lower and file_names:
        expected_content = _extract_expected_content(criterion)
        if expected_content is None:
            return False
        file_path = run_state.workspace_path / file_names[0]
        if not file_path.is_file():
            return False
        actual = file_path.read_text(encoding="utf-8")
        return actual == expected_content or actual == f"{expected_content}\n"

    if _mentions_progress_log(criterion_lower):
        if not _progress_log_was_updated(previous_snapshot, current_snapshot):
            return False
        if "contains" in criterion_lower:
            return _progress_mentions_expected_context(
                criterion=criterion_lower,
                story_text_lower=story_text_lower,
                progress_text=current_snapshot.progress_text.lower(),
            )
        if "append" in criterion_lower:
            return _progress_entry_count(current_snapshot.progress_text) > _progress_entry_count(previous_snapshot.progress_text)
        return bool(current_snapshot.progress_text.strip())

    skill_names = _extract_skill_names(criterion_lower or story_text_lower)
    if skill_names and "skill" in criterion_lower:
        return any(_skill_artifact_exists(run_state, skill_name, changed_paths) for skill_name in skill_names)

    if file_names and any(file_name in changed_paths for file_name in file_names):
        return True

    return False


def _mentions_progress_log(text: str) -> bool:
    return "progress log" in text or "progress.txt" in text


def _progress_log_was_updated(
    previous_snapshot: _WorkspaceSnapshot,
    current_snapshot: _WorkspaceSnapshot,
) -> bool:
    return current_snapshot.progress_text != previous_snapshot.progress_text


def _progress_entry_count(progress_text: str) -> int:
    return progress_text.count("\n## ")


def _progress_mentions_expected_context(
    *,
    criterion: str,
    story_text_lower: str,
    progress_text: str,
) -> bool:
    tokens = sorted(
        {
            *[name.lower() for name in _extract_file_names(f"{criterion}\n{story_text_lower}")],
            *[name.lower() for name in _extract_skill_names(f"{criterion}\n{story_text_lower}")],
        }
    )
    if not tokens:
        return bool(progress_text.strip())
    return all(token in progress_text for token in tokens)


def _skill_artifact_exists(
    run_state: RalphRunState,
    skill_name: str,
    changed_paths: set[str],
) -> bool:
    skill_root = run_state.workspace_path / ".deepagents" / "skills" / skill_name
    if not skill_root.exists():
        return False

    for file_path in skill_root.rglob("*"):
        if not file_path.is_file():
            continue
        if file_path.name == "SKILL.md":
            continue
        relative = file_path.relative_to(run_state.workspace_path).as_posix()
        if relative in changed_paths or file_path.exists():
            return True
    return False


def _extract_file_names(text: str) -> list[str]:
    return list(dict.fromkeys(re.findall(r"\b[\w./-]+\.[A-Za-z0-9_-]+\b", text)))


def _extract_skill_names(text: str) -> list[str]:
    matches = re.findall(r"\b([a-z0-9][a-z0-9-]*) skill\b", text)
    return list(dict.fromkeys(matches))


def _extract_expected_content(criterion: str) -> str | None:
    match = re.search(r'content equals exactly:\s*"([^"]*)"', criterion, flags=re.IGNORECASE)
    if match is None:
        return None
    return match.group(1)


def _plan_prd_from_requirement(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    api_key: str,
    planner_memory: str = "",
) -> _PlanningResult:
    project_name = request.project_name or run_state.workspace_path.name
    try:
        return _PlanningResult(
            prd=_call_planner(
                request=request,
                run_state=run_state,
                api_key=api_key,
                project_name=project_name,
                planner_memory=planner_memory,
            )
        )
    except Exception as exc:  # noqa: BLE001
        return _PlanningResult(
            prd=_build_fallback_prd(
                requirement=request.requirement,
                project_name=project_name,
            ),
            fallback_reason=f"{type(exc).__name__}: {exc}",
        )


def _call_plan_prd_from_requirement(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    api_key: str,
    planner_memory: str,
) -> _PlanningResult | RalphPrd:
    """兼容旧测试桩：planner_memory 参数缺失时回退到旧签名。"""

    try:
        return _plan_prd_from_requirement(
            request=request,
            run_state=run_state,
            api_key=api_key,
            planner_memory=planner_memory,
        )
    except TypeError as exc:
        if "planner_memory" not in str(exc):
            raise
        return _plan_prd_from_requirement(
            request=request,
            run_state=run_state,
            api_key=api_key,
        )


def _call_planner(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    api_key: str,
    project_name: str,
    planner_memory: str,
) -> RalphPrd:
    """兼容旧测试桩：当 monkeypatch 未声明 `project_name` 时回退到旧签名。"""

    try:
        return _plan_prd_with_model(
            request=request,
            run_state=run_state,
            api_key=api_key,
            project_name=project_name,
            planner_memory=planner_memory,
        )
    except TypeError as exc:
        message = str(exc)
        if "project_name" not in message and "planner_memory" not in message:
            raise
        return _plan_prd_with_model(
            request=request,
            run_state=run_state,
            api_key=api_key,
        )


def _plan_prd_with_model(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    api_key: str,
    project_name: str | None = None,
    planner_memory: str = "",
) -> RalphPrd:
    effective_project_name = project_name or run_state.workspace_path.name
    planner = ChatOpenAI(
        model=request.model,
        api_key=api_key,
        base_url=request.base_url,
        streaming=True,
        timeout=request.timeout,
        max_retries=1,
        use_responses_api=False,
    )
    messages = [
        SystemMessage(content=_build_planner_system_prompt()),
        HumanMessage(
            content=(
                f"Project name: {effective_project_name}\n"
                f"Workspace: {run_state.workspace_path}\n"
                "Prior memory from the persistent session file:\n"
                f"{planner_memory or '(none)'}\n\n"
                f"Requirement:\n{request.requirement}\n"
            )
        ),
    ]
    response = _collect_streamed_ai_message(planner, messages)
    content = _extract_text_content(response.content)
    raw = _extract_json_object(content)
    document = json.loads(raw)
    normalized = _normalize_prd(document, project_name=effective_project_name)
    return RalphPrd.model_validate(normalized)


def _build_fallback_prd(*, requirement: str, project_name: str) -> RalphPrd:
    description = requirement.strip() or "Implement requested change"
    return RalphPrd.model_validate(
        {
            "project": project_name,
            "branchName": f"ralph/{_slugify(description)}",
            "description": description,
            "userStories": [
                {
                    "id": "US-001",
                    "title": "Implement requested requirement",
                    "description": description,
                    "acceptanceCriteria": [
                        "Implement the requested change in the target workspace.",
                        "Run the relevant quality checks for the affected code.",
                        "If checks pass, update progress.txt and mark the story as passed.",
                    ],
                    "priority": 1,
                    "passes": False,
                    "notes": "Fallback PRD generated because automatic planning failed.",
                }
            ],
        }
    )


def _normalize_prd(document: dict[str, object], *, project_name: str) -> dict[str, object]:
    description = str(document.get("description") or "").strip()
    if not description:
        raise ValueError("planner did not return a description")

    branch_name = str(document.get("branchName") or "").strip()
    if not branch_name:
        branch_name = f"ralph/{_slugify(description)}"
    elif not branch_name.startswith("ralph/"):
        branch_name = f"ralph/{_slugify(branch_name)}"

    raw_stories = document.get("userStories")
    if not isinstance(raw_stories, list) or not raw_stories:
        raise ValueError("planner did not return any user stories")

    user_stories: list[dict[str, object]] = []
    for index, item in enumerate(raw_stories, start=1):
        if not isinstance(item, dict):
            raise ValueError("planner returned an invalid user story")
        acceptance = item.get("acceptanceCriteria")
        acceptance_list = acceptance if isinstance(acceptance, list) else []
        cleaned_acceptance = [str(entry).strip() for entry in acceptance_list if str(entry).strip()]
        if not cleaned_acceptance:
            raise ValueError("planner returned a story without acceptance criteria")
        user_stories.append(
            {
                "id": str(item.get("id") or f"US-{index:03d}").strip(),
                "title": str(item.get("title") or "").strip(),
                "description": str(item.get("description") or "").strip(),
                "acceptanceCriteria": cleaned_acceptance,
                "priority": int(item.get("priority") or index),
                "passes": bool(item.get("passes", False)),
                "notes": str(item.get("notes") or ""),
            }
        )

    return {
        "project": str(document.get("project") or project_name).strip(),
        "branchName": branch_name,
        "description": description,
        "userStories": user_stories,
    }


def _build_planner_system_prompt() -> str:
    return (
        "Convert the requirement into a Ralph-compatible JSON PRD.\n"
        "Use the prior memory to preserve ongoing user intent and project context.\n"
        "If the prior memory conflicts with the newest requirement, prefer the newest requirement.\n"
        "Return JSON only, with this exact top-level shape:\n"
        "{"
        '"project": string, '
        '"branchName": string, '
        '"description": string, '
        '"userStories": ['
        "{"
        '"id": string, '
        '"title": string, '
        '"description": string, '
        '"acceptanceCriteria": [string], '
        '"priority": integer, '
        '"passes": false, '
        '"notes": string'
        "}"
        "]"
        "}\n"
        "Rules:\n"
        "- Produce 1 to 5 user stories.\n"
        "- Each story must be small enough to complete in one iteration.\n"
        "- Priorities must start at 1 and increase without gaps.\n"
        "- branchName must start with 'ralph/'.\n"
        "- Keep acceptance criteria concrete and verifiable.\n"
        "- Set passes to false for every story.\n"
    )


def _render_memory_for_planner(messages: list[object]) -> str:
    lines: list[str] = []
    for message in messages:
        if isinstance(message, SystemMessage):
            content = _extract_text_content(message.content)
            if not content or content == DEFAULT_SYSTEM_PROMPT:
                continue
            lines.append(f"system: {content}")
            continue
        if isinstance(message, HumanMessage):
            content = _extract_text_content(message.content)
            if content:
                lines.append(f"user: {content}")
            continue
        if isinstance(message, AIMessage):
            content = _extract_text_content(message.content)
            if content:
                lines.append(f"assistant: {content}")
    return "\n".join(lines).strip()


def _append_ralph_result_to_memory(
    *,
    session_manager: SessionManager,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    status: str,
    iteration: int,
) -> None:
    prd = _read_prd(run_state.prd_path)
    completed = [story.id for story in prd.sorted_stories() if story.passes]
    remaining = [story.id for story in prd.sorted_stories() if not story.passes]
    progress_summary = _read_last_progress_entry(run_state.progress_path)
    summary = (
        "Persistent orchestrator memory\n"
        "The following Ralph task result was recorded by the system after checking workspace state.\n"
        "Treat it as factual session memory about completed work, not as a hypothetical plan.\n\n"
        "Ralph task result\n"
        f"Requirement: {request.requirement}\n"
        f"Status: {status}\n"
        f"Iterations used: {iteration}\n"
        f"Completed stories: {', '.join(completed) if completed else '(none)'}\n"
        f"Remaining stories: {', '.join(remaining) if remaining else '(none)'}\n"
        f"Run id: {run_state.run_id}\n"
        f"Run directory: {run_state.run_dir}\n"
        f"Latest progress entry:\n{progress_summary}"
    )
    session_manager.append_custom_entry(
        custom_type="ralph_result",
        data={
            "requirement": request.requirement,
            "status": status,
            "iteration": iteration,
            "completed_story_ids": completed,
            "remaining_story_ids": remaining,
            "run_id": run_state.run_id,
            "run_dir": str(run_state.run_dir),
            "progress_path": str(run_state.progress_path),
            "prd_path": str(run_state.prd_path),
        },
    )
    session_manager.append_custom_message_entry(
        custom_type="ralph_result_memory",
        content=summary,
        display=False,
        role="system",
    )


def _read_last_progress_entry(progress_path: Path) -> str:
    if not progress_path.exists():
        return "(progress log not found)"

    progress_text = progress_path.read_text(encoding="utf-8").strip()
    if not progress_text:
        return "(progress log is empty)"

    parts = [part.strip() for part in progress_text.split("\n---") if part.strip()]
    return parts[-1] if parts else progress_text


def _build_iteration_system_prompt(run_state: RalphRunState) -> str:
    return (
        "You are an autonomous coding agent working on a software project.\n\n"
        "Your durable memory between iterations is outside the chat context and "
        "lives in:\n"
        f"- Git history in the workspace {run_state.workspace_path}\n"
        f"- PRD file: {run_state.prd_path}\n"
        f"- Progress log: {run_state.progress_path}\n\n"
        "Your task on each iteration:\n"
        f"1. Read the PRD at {run_state.prd_path}.\n"
        f"2. Read the progress log at {run_state.progress_path} and check the "
        "Codebase Patterns section first if it exists.\n"
        "3. Check you are on the correct branch from PRD branchName. If not, "
        "check it out or create it from main.\n"
        "4. Pick the highest priority user story where passes is false.\n"
        "5. Implement only that single story.\n"
        "6. Run quality checks appropriate for the project.\n"
        "7. Update nearby AGENTS.md files if you discover reusable patterns.\n"
        "8. If checks pass, commit changes with message: "
        "`feat: [Story ID] - [Story Title]`.\n"
        "9. Update the PRD to set passes to true for the completed story.\n"
        "10. Append progress to progress.txt.\n\n"
        "Progress report format in progress.txt:\n"
        "## [Date/Time] - [Story ID]\n"
        "- What was implemented\n"
        "- Files changed\n"
        "- Learnings for future iterations:\n"
        "  - Patterns discovered\n"
        "  - Gotchas encountered\n"
        "  - Useful context\n"
        "---\n\n"
        "Important rules:\n"
        "- Work on one story per iteration.\n"
        "- Use absolute file paths for read and write tools.\n"
        f"- Use bash with cwd set to {run_state.workspace_path} when operating "
        "in the repo.\n"
        "- Do not rely on previous chat history. It will be discarded.\n"
        f"- If all stories pass, end your final response with exactly "
        f"{_COMPLETE_MARKER}.\n"
    )


def _build_iteration_user_prompt(
    *,
    request: RalphStreamRequest,
    run_state: RalphRunState,
    iteration: int,
    max_iterations: int,
) -> str:
    return (
        f"Ralph iteration {iteration} of {max_iterations}.\n"
        f"Workspace path: {run_state.workspace_path}\n"
        f"Run directory: {run_state.run_dir}\n"
        f"PRD path: {run_state.prd_path}\n"
        f"Progress path: {run_state.progress_path}\n"
        "Start by reading the PRD and progress files.\n"
        "Requirement summary for this run:\n"
        f"{request.requirement}\n"
    )


def _extract_json_object(text: str) -> str:
    if not text.strip():
        raise ValueError("planner returned empty content")

    fenced = re.search(r"```(?:json)?\s*(\{.*\})\s*```", text, flags=re.DOTALL)
    if fenced is not None:
        return fenced.group(1)

    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise ValueError("planner did not return JSON")
    return text[start : end + 1]


def _slugify(value: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
    return slug or "task"


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


def _collect_streamed_ai_message(model: object, messages: list[object]) -> AIMessage:
    """Collect a streamed model response into a single AIMessage."""

    chunks: list[object] = []
    for chunk in model.stream(messages):
        chunks.append(chunk)

    if not chunks:
        raise RuntimeError("planner stream returned no chunks")

    merged = chunks[0]
    for chunk in chunks[1:]:
        merged = merged + chunk

    if isinstance(merged, AIMessage):
        return merged

    if hasattr(merged, "to_message"):
        converted = merged.to_message()
        if isinstance(converted, AIMessage):
            return converted

    content = getattr(merged, "content", "")
    tool_calls = getattr(merged, "tool_calls", [])
    additional_kwargs = getattr(merged, "additional_kwargs", {})
    response_metadata = getattr(merged, "response_metadata", {})
    message_id = getattr(merged, "id", None)
    return AIMessage(
        content=content,
        tool_calls=tool_calls,
        additional_kwargs=additional_kwargs,
        response_metadata=response_metadata,
        id=message_id,
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


def _find_last_ai_message(messages: list[object]) -> AIMessage | None:
    for message in reversed(messages):
        if isinstance(message, AIMessage):
            return message
    return None


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


def _sse(event: str, data: dict[str, object]) -> str:
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"
