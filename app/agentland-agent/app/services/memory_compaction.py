from __future__ import annotations

"""pi-mono 风格的会话记忆压缩。"""

import json
import math
import os
from dataclasses import dataclass
from typing import cast

from langchain_core.messages import AIMessage, AnyMessage, HumanMessage, SystemMessage, ToolMessage
from langchain_openai import ChatOpenAI

from app.services.session_memory import JsonValue, SessionManager

_TOOL_RESULT_MAX_CHARS = 2000
_DEFAULT_CONTEXT_WINDOW = 128000
_CONTEXT_WINDOW_ENV = "AGENTLAND_CONTEXT_WINDOW"
_COMPACTION_ENABLED_ENV = "AGENTLAND_COMPACTION_ENABLED"
_RESERVE_TOKENS_ENV = "AGENTLAND_COMPACTION_RESERVE_TOKENS"
_KEEP_RECENT_TOKENS_ENV = "AGENTLAND_COMPACTION_KEEP_RECENT_TOKENS"

_SUMMARIZATION_SYSTEM_PROMPT = (
    "You are a context summarization assistant. Your task is to read a conversation between a user and an AI "
    "coding assistant, then produce a structured summary following the exact format specified.\n\n"
    "Do NOT continue the conversation. Do NOT respond to any questions in the conversation. "
    "ONLY output the structured summary."
)

_SUMMARIZATION_PROMPT = """The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages."""

_UPDATE_SUMMARIZATION_PROMPT = """The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages."""

_TURN_PREFIX_SUMMARIZATION_PROMPT = """This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix."""


@dataclass(slots=True)
class CompactionSettings:
    enabled: bool = True
    reserve_tokens: int = 16384
    keep_recent_tokens: int = 20000


@dataclass(slots=True)
class ContextUsageEstimate:
    tokens: int
    usage_tokens: int
    trailing_tokens: int
    last_usage_index: int | None


@dataclass(slots=True)
class FileOperations:
    read: set[str]
    written: set[str]
    edited: set[str]


@dataclass(slots=True)
class CutPointResult:
    first_kept_entry_index: int
    turn_start_index: int
    is_split_turn: bool


@dataclass(slots=True)
class CompactionPreparation:
    first_kept_entry_id: str
    messages_to_summarize: list[AnyMessage]
    turn_prefix_messages: list[AnyMessage]
    is_split_turn: bool
    tokens_before: int
    previous_summary: str | None
    file_ops: FileOperations
    settings: CompactionSettings


@dataclass(slots=True)
class CompactionResult:
    summary: str
    first_kept_entry_id: str
    tokens_before: int
    details: dict[str, JsonValue]


def load_compaction_settings() -> CompactionSettings:
    return CompactionSettings(
        enabled=_env_bool(_COMPACTION_ENABLED_ENV, True),
        reserve_tokens=_env_int(_RESERVE_TOKENS_ENV, 16384),
        keep_recent_tokens=_env_int(_KEEP_RECENT_TOKENS_ENV, 20000),
    )


def resolve_context_window() -> int:
    return _env_int(_CONTEXT_WINDOW_ENV, _DEFAULT_CONTEXT_WINDOW)


def should_compact(context_tokens: int, context_window: int, settings: CompactionSettings) -> bool:
    if not settings.enabled:
        return False
    return context_tokens > context_window - settings.reserve_tokens


def detect_context_overflow(exc: Exception) -> bool:
    text = str(exc).lower()
    return any(
        marker in text
        for marker in (
            "context length",
            "maximum context length",
            "maximum context window",
            "context window",
            "too many tokens",
            "prompt is too long",
        )
    )


def estimate_context_tokens(messages: list[AnyMessage]) -> ContextUsageEstimate:
    usage_info = _get_last_assistant_usage_info(messages)
    if usage_info is None:
        estimated = sum(estimate_tokens(message) for message in messages)
        return ContextUsageEstimate(
            tokens=estimated,
            usage_tokens=0,
            trailing_tokens=estimated,
            last_usage_index=None,
        )

    usage_tokens, index = usage_info
    trailing_tokens = sum(estimate_tokens(message) for message in messages[index + 1 :])
    return ContextUsageEstimate(
        tokens=usage_tokens + trailing_tokens,
        usage_tokens=usage_tokens,
        trailing_tokens=trailing_tokens,
        last_usage_index=index,
    )


def maybe_auto_compact(
    *,
    manager: SessionManager,
    model: str,
    api_key: str,
    base_url: str | None,
    timeout: float,
) -> CompactionResult | None:
    settings = load_compaction_settings()
    if not settings.enabled:
        return None

    context_tokens = estimate_context_tokens(manager.build_session_context()).tokens
    if not should_compact(context_tokens, resolve_context_window(), settings):
        return None

    return compact_session(
        manager=manager,
        model=model,
        api_key=api_key,
        base_url=base_url,
        timeout=timeout,
        settings=settings,
    )


def compact_session(
    *,
    manager: SessionManager,
    model: str,
    api_key: str,
    base_url: str | None,
    timeout: float,
    settings: CompactionSettings | None = None,
    custom_instructions: str | None = None,
) -> CompactionResult | None:
    effective_settings = settings or load_compaction_settings()
    preparation = prepare_compaction(manager.get_branch(), effective_settings)
    if preparation is None:
        return None

    result = build_compaction_result(
        preparation=preparation,
        model=model,
        api_key=api_key,
        base_url=base_url,
        timeout=timeout,
        custom_instructions=custom_instructions,
    )
    manager.append_compaction(
        summary=result.summary,
        first_kept_entry_id=result.first_kept_entry_id,
        tokens_before=result.tokens_before,
        details=result.details,
    )
    return result


def prepare_compaction(
    entries: list[dict[str, object]],
    settings: CompactionSettings,
) -> CompactionPreparation | None:
    if not entries:
        return None
    if str(entries[-1].get("type")) == "compaction":
        return None

    prev_compaction_index = _find_previous_compaction_index(entries)
    boundary_start = prev_compaction_index + 1
    if prev_compaction_index < 0:
        boundary_start = _skip_leading_system_messages(entries, boundary_start)
    boundary_end = len(entries)
    if boundary_start >= boundary_end:
        return None

    usage_start = prev_compaction_index if prev_compaction_index >= 0 else 0
    usage_messages = [
        message
        for entry in entries[usage_start:boundary_end]
        if (message := _get_message_from_entry(entry)) is not None
    ]
    tokens_before = estimate_context_tokens(usage_messages).tokens

    cut_point = find_cut_point(entries, boundary_start, boundary_end, settings.keep_recent_tokens)
    first_kept_entry = entries[cut_point.first_kept_entry_index]
    first_kept_entry_id = str(first_kept_entry["id"])

    history_end = cut_point.turn_start_index if cut_point.is_split_turn else cut_point.first_kept_entry_index
    messages_to_summarize = [
        message
        for entry in entries[boundary_start:history_end]
        if (message := _get_message_from_entry(entry)) is not None
    ]

    turn_prefix_messages: list[AnyMessage] = []
    if cut_point.is_split_turn:
        for entry in entries[cut_point.turn_start_index : cut_point.first_kept_entry_index]:
            message = _get_message_from_entry(entry)
            if message is not None:
                turn_prefix_messages.append(message)

    if not messages_to_summarize and not turn_prefix_messages:
        return None

    previous_summary: str | None = None
    if prev_compaction_index >= 0:
        previous_summary = str(entries[prev_compaction_index].get("summary", "")).strip() or None

    file_ops = _extract_file_operations(
        messages=messages_to_summarize,
        entries=entries,
        prev_compaction_index=prev_compaction_index,
    )
    for message in turn_prefix_messages:
        extract_file_ops_from_message(message, file_ops)

    return CompactionPreparation(
        first_kept_entry_id=first_kept_entry_id,
        messages_to_summarize=messages_to_summarize,
        turn_prefix_messages=turn_prefix_messages,
        is_split_turn=cut_point.is_split_turn,
        tokens_before=tokens_before,
        previous_summary=previous_summary,
        file_ops=file_ops,
        settings=settings,
    )


def build_compaction_result(
    *,
    preparation: CompactionPreparation,
    model: str,
    api_key: str,
    base_url: str | None,
    timeout: float,
    custom_instructions: str | None = None,
) -> CompactionResult:
    if preparation.is_split_turn and preparation.turn_prefix_messages:
        history_summary = (
            generate_summary(
                current_messages=preparation.messages_to_summarize,
                model=model,
                reserve_tokens=preparation.settings.reserve_tokens,
                api_key=api_key,
                base_url=base_url,
                timeout=timeout,
                custom_instructions=custom_instructions,
                previous_summary=preparation.previous_summary,
            )
            if preparation.messages_to_summarize
            else "No prior history."
        )
        turn_prefix_summary = generate_turn_prefix_summary(
            messages=preparation.turn_prefix_messages,
            model=model,
            reserve_tokens=preparation.settings.reserve_tokens,
            api_key=api_key,
            base_url=base_url,
            timeout=timeout,
        )
        summary = f"{history_summary}\n\n---\n\n**Turn Context (split turn):**\n\n{turn_prefix_summary}"
    else:
        summary = generate_summary(
            current_messages=preparation.messages_to_summarize,
            model=model,
            reserve_tokens=preparation.settings.reserve_tokens,
            api_key=api_key,
            base_url=base_url,
            timeout=timeout,
            custom_instructions=custom_instructions,
            previous_summary=preparation.previous_summary,
        )

    read_files, modified_files = compute_file_lists(preparation.file_ops)
    summary += format_file_operations(read_files, modified_files)
    return CompactionResult(
        summary=summary,
        first_kept_entry_id=preparation.first_kept_entry_id,
        tokens_before=preparation.tokens_before,
        details={
            "readFiles": read_files,
            "modifiedFiles": modified_files,
        },
    )


def generate_summary(
    *,
    current_messages: list[AnyMessage],
    model: str,
    reserve_tokens: int,
    api_key: str,
    base_url: str | None,
    timeout: float,
    custom_instructions: str | None = None,
    previous_summary: str | None = None,
) -> str:
    base_prompt = _UPDATE_SUMMARIZATION_PROMPT if previous_summary else _SUMMARIZATION_PROMPT
    if custom_instructions:
        base_prompt = f"{base_prompt}\n\nAdditional focus: {custom_instructions}"

    conversation_text = serialize_conversation(current_messages)
    prompt_text = f"<conversation>\n{conversation_text}\n</conversation>\n\n"
    if previous_summary:
        prompt_text += f"<previous-summary>\n{previous_summary}\n</previous-summary>\n\n"
    prompt_text += base_prompt
    return _generate_summary_text(
        model=model,
        api_key=api_key,
        base_url=base_url,
        timeout=timeout,
        prompt_text=prompt_text,
        max_tokens=max(256, math.floor(0.8 * reserve_tokens)),
    )


def generate_turn_prefix_summary(
    *,
    messages: list[AnyMessage],
    model: str,
    reserve_tokens: int,
    api_key: str,
    base_url: str | None,
    timeout: float,
) -> str:
    conversation_text = serialize_conversation(messages)
    prompt_text = f"<conversation>\n{conversation_text}\n</conversation>\n\n{_TURN_PREFIX_SUMMARIZATION_PROMPT}"
    return _generate_summary_text(
        model=model,
        api_key=api_key,
        base_url=base_url,
        timeout=timeout,
        prompt_text=prompt_text,
        max_tokens=max(256, math.floor(0.5 * reserve_tokens)),
    )


def serialize_conversation(messages: list[AnyMessage]) -> str:
    parts: list[str] = []
    for message in messages:
        if isinstance(message, HumanMessage):
            content = _message_text(message)
            if content:
                parts.append(f"[User]: {content}")
            continue
        if isinstance(message, SystemMessage):
            content = _message_text(message)
            if content:
                parts.append(f"[System]: {content}")
            continue
        if isinstance(message, AIMessage):
            text_parts = [_extract_text_content_block(block) for block in _iter_message_blocks(message.content)]
            text = "\n".join(part for part in text_parts if part)
            if text:
                parts.append(f"[Assistant]: {text}")
            tool_calls = _serialize_tool_calls(message)
            if tool_calls:
                parts.append(f"[Assistant tool calls]: {'; '.join(tool_calls)}")
            continue
        if isinstance(message, ToolMessage):
            content = _truncate_for_summary(_message_text(message), _TOOL_RESULT_MAX_CHARS)
            if content:
                parts.append(f"[Tool result]: {content}")
    return "\n\n".join(parts)


def create_file_ops() -> FileOperations:
    return FileOperations(read=set(), written=set(), edited=set())


def extract_file_ops_from_message(message: AnyMessage, file_ops: FileOperations) -> None:
    if not isinstance(message, AIMessage):
        return
    for tool_call in cast(list[dict[str, object]], message.tool_calls or []):
        name = str(tool_call.get("name") or "")
        args = tool_call.get("args")
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except json.JSONDecodeError:
                args = {}
        if not isinstance(args, dict):
            continue
        path = args.get("path")
        if not isinstance(path, str) or not path:
            continue
        if name == "read":
            file_ops.read.add(path)
        elif name == "write":
            file_ops.written.add(path)
        elif name == "edit":
            file_ops.edited.add(path)


def compute_file_lists(file_ops: FileOperations) -> tuple[list[str], list[str]]:
    modified = set(file_ops.written) | set(file_ops.edited)
    read_only = sorted(path for path in file_ops.read if path not in modified)
    return read_only, sorted(modified)


def format_file_operations(read_files: list[str], modified_files: list[str]) -> str:
    sections: list[str] = []
    if read_files:
        sections.append("<read-files>\n" + "\n".join(read_files) + "\n</read-files>")
    if modified_files:
        sections.append("<modified-files>\n" + "\n".join(modified_files) + "\n</modified-files>")
    if not sections:
        return ""
    return "\n\n" + "\n\n".join(sections)


def estimate_tokens(message: AnyMessage) -> int:
    if isinstance(message, ToolMessage):
        return math.ceil(len(_message_text(message)) / 4)
    if isinstance(message, (HumanMessage, SystemMessage)):
        return math.ceil(len(_message_text(message)) / 4)
    if isinstance(message, AIMessage):
        chars = len(_message_text(message))
        for tool_call in cast(list[dict[str, object]], message.tool_calls or []):
            chars += len(str(tool_call.get("name") or ""))
            chars += len(json.dumps(tool_call.get("args", {}), ensure_ascii=False))
        return math.ceil(chars / 4)
    return 0


def find_cut_point(
    entries: list[dict[str, object]],
    start_index: int,
    end_index: int,
    keep_recent_tokens: int,
) -> CutPointResult:
    cut_points = _find_valid_cut_points(entries, start_index, end_index)
    if not cut_points:
        return CutPointResult(
            first_kept_entry_index=start_index,
            turn_start_index=-1,
            is_split_turn=False,
        )

    accumulated_tokens = 0
    cut_index = cut_points[0]

    for index in range(end_index - 1, start_index - 1, -1):
        message = _get_message_from_entry(entries[index])
        if message is None:
            continue
        accumulated_tokens += estimate_tokens(message)
        if accumulated_tokens >= keep_recent_tokens:
            for cut_point in cut_points:
                if cut_point >= index:
                    cut_index = cut_point
                    break
            break

    while cut_index > start_index:
        previous_entry = entries[cut_index - 1]
        if str(previous_entry.get("type")) == "compaction":
            break
        if str(previous_entry.get("type")) == "message":
            break
        cut_index -= 1

    cut_entry = entries[cut_index]
    is_user_message = _entry_is_user_message(cut_entry)
    turn_start_index = -1 if is_user_message else _find_turn_start_index(entries, cut_index, start_index)
    return CutPointResult(
        first_kept_entry_index=cut_index,
        turn_start_index=turn_start_index,
        is_split_turn=not is_user_message and turn_start_index != -1,
    )


def _generate_summary_text(
    *,
    model: str,
    api_key: str,
    base_url: str | None,
    timeout: float,
    prompt_text: str,
    max_tokens: int,
) -> str:
    llm = ChatOpenAI(
        model=model,
        api_key=api_key,
        base_url=base_url,
        streaming=True,
        timeout=timeout,
        max_retries=1,
        max_tokens=max_tokens,
        use_responses_api=False,
    )
    messages: list[AnyMessage] = [
        SystemMessage(content=_SUMMARIZATION_SYSTEM_PROMPT),
        HumanMessage(content=prompt_text),
    ]
    chunks: list[object] = []
    for chunk in llm.stream(messages):
        chunks.append(chunk)

    if not chunks:
        response = llm.invoke(messages)
        if isinstance(response, AIMessage):
            return _message_text(response).strip()
        raise RuntimeError("compaction summarizer returned no content")

    merged = chunks[0]
    for chunk in chunks[1:]:
        merged = merged + chunk

    if isinstance(merged, AIMessage):
        return _message_text(merged).strip()
    if hasattr(merged, "to_message"):
        converted = merged.to_message()
        if isinstance(converted, AIMessage):
            return _message_text(converted).strip()
    content = getattr(merged, "content", "")
    if isinstance(content, str):
        return content.strip()
    return _extract_text(content).strip()


def _get_message_from_entry(entry: dict[str, object]) -> AnyMessage | None:
    entry_type = str(entry.get("type"))
    if entry_type == "message":
        message = cast(dict[str, object], entry.get("message", {}))
        return _deserialize_entry_message(message)
    if entry_type == "custom_message":
        role = str(entry.get("role", "system"))
        content = entry.get("content", "")
        if role == "user":
            return HumanMessage(content=content)
        if role == "assistant":
            return AIMessage(content=content)
        return SystemMessage(content=content)
    if entry_type == "branch_summary":
        return SystemMessage(content=f"Branch summary:\n{entry.get('summary', '')}")
    if entry_type == "compaction":
        return SystemMessage(content=f"Compaction summary:\n{entry.get('summary', '')}")
    return None


def _deserialize_entry_message(payload: dict[str, object]) -> AnyMessage:
    role = str(payload.get("role", ""))
    content = payload.get("content", "")
    if role == "system":
        return SystemMessage(content=content)
    if role == "user":
        return HumanMessage(content=content)
    if role == "tool":
        return ToolMessage(
            content=content,
            tool_call_id=str(payload.get("tool_call_id", "")),
            name=_optional_str(payload.get("name")),
        )
    if role == "assistant":
        return AIMessage(
            content=content,
            tool_calls=cast(list[dict[str, object]], payload.get("tool_calls", [])),
            additional_kwargs=cast(dict[str, object], payload.get("additional_kwargs", {})),
            response_metadata=cast(dict[str, object], payload.get("response_metadata", {})),
            id=_optional_str(payload.get("message_id")),
            name=_optional_str(payload.get("name")),
        )
    raise ValueError(f"unsupported message role: {role}")


def _extract_file_operations(
    *,
    messages: list[AnyMessage],
    entries: list[dict[str, object]],
    prev_compaction_index: int,
) -> FileOperations:
    file_ops = create_file_ops()
    if prev_compaction_index >= 0:
        details = entries[prev_compaction_index].get("details")
        if isinstance(details, dict):
            read_files = details.get("readFiles")
            modified_files = details.get("modifiedFiles")
            if isinstance(read_files, list):
                for path in read_files:
                    if isinstance(path, str):
                        file_ops.read.add(path)
            if isinstance(modified_files, list):
                for path in modified_files:
                    if isinstance(path, str):
                        file_ops.edited.add(path)

    for message in messages:
        extract_file_ops_from_message(message, file_ops)
    return file_ops


def _find_previous_compaction_index(entries: list[dict[str, object]]) -> int:
    for index in range(len(entries) - 1, -1, -1):
        if str(entries[index].get("type")) == "compaction":
            return index
    return -1


def _skip_leading_system_messages(entries: list[dict[str, object]], start_index: int) -> int:
    index = start_index
    while index < len(entries):
        entry = entries[index]
        if str(entry.get("type")) != "message":
            break
        message = cast(dict[str, object], entry.get("message", {}))
        if str(message.get("role")) != "system":
            break
        index += 1
    return index


def _find_valid_cut_points(entries: list[dict[str, object]], start_index: int, end_index: int) -> list[int]:
    cut_points: list[int] = []
    for index in range(start_index, end_index):
        entry = entries[index]
        entry_type = str(entry.get("type"))
        if entry_type == "message":
            role = str(cast(dict[str, object], entry.get("message", {})).get("role"))
            if role in {"user", "assistant"}:
                cut_points.append(index)
            continue
        if entry_type in {"custom_message", "branch_summary"}:
            cut_points.append(index)
    return cut_points


def _find_turn_start_index(entries: list[dict[str, object]], entry_index: int, start_index: int) -> int:
    for index in range(entry_index, start_index - 1, -1):
        entry = entries[index]
        entry_type = str(entry.get("type"))
        if entry_type in {"custom_message", "branch_summary"}:
            return index
        if entry_type == "message":
            role = str(cast(dict[str, object], entry.get("message", {})).get("role"))
            if role == "user":
                return index
    return -1


def _entry_is_user_message(entry: dict[str, object]) -> bool:
    if str(entry.get("type")) != "message":
        return False
    return str(cast(dict[str, object], entry.get("message", {})).get("role")) == "user"


def _get_last_assistant_usage_info(messages: list[AnyMessage]) -> tuple[int, int] | None:
    for index in range(len(messages) - 1, -1, -1):
        message = messages[index]
        if not isinstance(message, AIMessage):
            continue
        usage_tokens = _assistant_usage_tokens(message)
        if usage_tokens is not None:
            return usage_tokens, index
    return None


def _assistant_usage_tokens(message: AIMessage) -> int | None:
    usage_metadata = getattr(message, "usage_metadata", None)
    if isinstance(usage_metadata, dict):
        total = usage_metadata.get("total_tokens")
        if isinstance(total, int) and total > 0:
            return total
        input_tokens = usage_metadata.get("input_tokens")
        output_tokens = usage_metadata.get("output_tokens")
        if isinstance(input_tokens, int) and isinstance(output_tokens, int):
            return input_tokens + output_tokens

    response_metadata = message.response_metadata
    if isinstance(response_metadata, dict):
        for key in ("token_usage", "usage"):
            usage = response_metadata.get(key)
            if not isinstance(usage, dict):
                continue
            total = usage.get("total_tokens")
            if isinstance(total, int) and total > 0:
                return total
            prompt_tokens = usage.get("prompt_tokens")
            completion_tokens = usage.get("completion_tokens")
            if isinstance(prompt_tokens, int) and isinstance(completion_tokens, int):
                return prompt_tokens + completion_tokens
    return None


def _serialize_tool_calls(message: AIMessage) -> list[str]:
    rendered: list[str] = []
    for tool_call in cast(list[dict[str, object]], message.tool_calls or []):
        name = str(tool_call.get("name") or "")
        args = tool_call.get("args", {})
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except json.JSONDecodeError:
                args = {"raw": args}
        if isinstance(args, dict):
            args_str = ", ".join(f"{key}={json.dumps(value, ensure_ascii=False)}" for key, value in args.items())
        else:
            args_str = json.dumps(args, ensure_ascii=False)
        rendered.append(f"{name}({args_str})")
    return rendered


def _message_text(message: AnyMessage) -> str:
    if isinstance(message.content, str):
        return message.content
    return _extract_text(message.content)


def _extract_text(content: object) -> str:
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""

    parts: list[str] = []
    for block in content:
        parts.append(_extract_text_content_block(block))
    return "".join(part for part in parts if part)


def _iter_message_blocks(content: object) -> list[object]:
    if isinstance(content, list):
        return content
    if content is None:
        return []
    return [content]


def _extract_text_content_block(block: object) -> str:
    if isinstance(block, str):
        return block
    if isinstance(block, dict):
        text = block.get("text")
        if isinstance(text, str):
            return text
        nested = block.get("content")
        if isinstance(nested, str):
            return nested
        if isinstance(nested, list):
            return _extract_text(nested)
        return ""
    text_attr = getattr(block, "text", None)
    if isinstance(text_attr, str):
        return text_attr
    content_attr = getattr(block, "content", None)
    if isinstance(content_attr, str):
        return content_attr
    return ""


def _truncate_for_summary(text: str, max_chars: int) -> str:
    if len(text) <= max_chars:
        return text
    truncated_chars = len(text) - max_chars
    return f"{text[:max_chars]}\n\n[... {truncated_chars} more characters truncated]"


def _optional_str(value: object) -> str | None:
    return value if isinstance(value, str) else None


def _env_int(name: str, default: int) -> int:
    value = os.getenv(name)
    if value is None:
        return default
    try:
        return int(value)
    except ValueError:
        return default


def _env_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() not in {"0", "false", "no", "off"}
