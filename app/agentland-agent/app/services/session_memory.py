from __future__ import annotations

"""pi-mono 风格的 append-only JSONL 会话存储。"""

import json
import os
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Literal, cast

from langchain_core.messages import AIMessage, AnyMessage, HumanMessage, SystemMessage, ToolMessage

type JsonValue = None | bool | int | float | str | list["JsonValue"] | dict[str, "JsonValue"]

SESSION_VERSION = 3
SESSION_ROOT_ENV = "PI_SESSION_ROOT"
_DEFAULT_SESSION_ROOT = Path.home() / ".pi" / "agent" / "sessions"


@dataclass(slots=True)
class SessionHeader:
    """会话头信息。"""

    session_id: str
    cwd: Path
    timestamp: str
    version: int = SESSION_VERSION


class SessionManager:
    """基于 JSONL 的树状会话管理器。"""

    __slots__ = ("_cwd", "_entries", "_header", "_leaf_id", "_ordered_ids", "_session_file")

    def __init__(
        self,
        *,
        header: SessionHeader,
        session_file: Path,
        entries: dict[str, dict[str, object]],
        ordered_ids: list[str],
        leaf_id: str | None,
    ) -> None:
        self._header = header
        self._session_file = session_file
        self._entries = entries
        self._ordered_ids = ordered_ids
        self._leaf_id = leaf_id
        self._cwd = header.cwd

    @classmethod
    def open_or_create(
        cls,
        *,
        cwd: Path,
        session_id: str | None,
        system_prompt: str,
        storage_root: Path | None = None,
    ) -> "SessionManager":
        """按 cwd/session_id 打开会话；不存在则创建。"""

        resolved_cwd = cwd.expanduser().resolve()
        resolved_root = _resolve_session_root(storage_root)
        session_dir = resolved_root / _cwd_bucket_name(resolved_cwd)
        session_dir.mkdir(parents=True, exist_ok=True)

        target_session_id = session_id or f"session-{uuid.uuid4().hex[:8]}"
        existing_file = _find_session_file(session_dir, target_session_id)
        if existing_file is None:
            manager = cls._create(session_dir=session_dir, cwd=resolved_cwd, session_id=target_session_id)
        else:
            manager = cls.open(existing_file)

        if not manager._ordered_ids:
            manager.append_message(SystemMessage(content=system_prompt))
        return manager

    @classmethod
    def open(cls, session_file: Path) -> "SessionManager":
        """打开已有会话文件。"""

        lines = session_file.read_text(encoding="utf-8").splitlines()
        if not lines:
            raise ValueError(f"empty session file: {session_file}")

        raw_header = json.loads(lines[0])
        header = SessionHeader(
            session_id=str(raw_header["id"]),
            cwd=Path(str(raw_header["cwd"])).expanduser().resolve(),
            timestamp=str(raw_header["timestamp"]),
            version=int(raw_header.get("version", SESSION_VERSION)),
        )

        entries: dict[str, dict[str, object]] = {}
        ordered_ids: list[str] = []
        leaf_id: str | None = None

        for line in lines[1:]:
            if not line.strip():
                continue
            entry = cast(dict[str, object], json.loads(line))
            entry_id = str(entry["id"])
            entries[entry_id] = entry
            ordered_ids.append(entry_id)
            leaf_id = entry_id

        return cls(
            header=header,
            session_file=session_file,
            entries=entries,
            ordered_ids=ordered_ids,
            leaf_id=leaf_id,
        )

    @classmethod
    def _create(cls, *, session_dir: Path, cwd: Path, session_id: str) -> "SessionManager":
        timestamp = _now_iso()
        session_file = session_dir / f"{_timestamp_slug(timestamp)}_{session_id}.jsonl"
        header = SessionHeader(session_id=session_id, cwd=cwd, timestamp=timestamp)
        session_file.write_text(
            json.dumps(
                {
                    "type": "session",
                    "version": header.version,
                    "id": header.session_id,
                    "timestamp": header.timestamp,
                    "cwd": str(header.cwd),
                },
                ensure_ascii=False,
            )
            + "\n",
            encoding="utf-8",
        )
        return cls(
            header=header,
            session_file=session_file,
            entries={},
            ordered_ids=[],
            leaf_id=None,
        )

    @property
    def cwd(self) -> Path:
        return self._cwd

    @property
    def session_file(self) -> Path:
        return self._session_file

    @property
    def session_id(self) -> str:
        return self._header.session_id

    def get_leaf_id(self) -> str | None:
        return self._leaf_id

    def get_entry(self, entry_id: str) -> dict[str, object] | None:
        return self._entries.get(entry_id)

    def get_entries(self) -> list[dict[str, object]]:
        return [self._entries[entry_id] for entry_id in self._ordered_ids]

    def get_branch(self, from_id: str | None = None) -> list[dict[str, object]]:
        """返回 root 到目标 leaf 的路径。"""

        target_id = from_id or self._leaf_id
        if target_id is None:
            return []

        branch: list[dict[str, object]] = []
        current_id: str | None = target_id
        while current_id is not None:
            entry = self._entries.get(current_id)
            if entry is None:
                raise ValueError(f"missing session entry: {current_id}")
            branch.append(entry)
            parent_id = entry.get("parentId")
            current_id = str(parent_id) if isinstance(parent_id, str) else None

        branch.reverse()
        return branch

    def branch(self, entry_id: str) -> None:
        """将当前 leaf 切换到既有节点。"""

        if entry_id not in self._entries:
            raise ValueError(f"unknown session entry: {entry_id}")
        self._leaf_id = entry_id

    def append_message(self, message: AnyMessage) -> str:
        """追加普通消息。"""

        return self._append_entry(
            {
                "type": "message",
                "message": _serialize_message(message),
            }
        )

    def append_compaction(
        self,
        *,
        summary: str,
        first_kept_entry_id: str,
        tokens_before: int,
        details: dict[str, JsonValue] | None = None,
        from_hook: bool | None = None,
    ) -> str:
        """追加 compaction 记录。"""

        entry: dict[str, object] = {
            "type": "compaction",
            "summary": summary,
            "firstKeptEntryId": first_kept_entry_id,
            "tokensBefore": tokens_before,
        }
        if details is not None:
            entry["details"] = details
        if from_hook is not None:
            entry["fromHook"] = from_hook
        return self._append_entry(entry)

    def append_custom_entry(
        self,
        *,
        custom_type: str,
        data: dict[str, JsonValue] | list[JsonValue] | JsonValue = None,
    ) -> str:
        """追加仅持久化、不参与上下文的自定义状态。"""

        return self._append_entry(
            {
                "type": "custom",
                "customType": custom_type,
                "data": data,
            }
        )

    def append_custom_message_entry(
        self,
        *,
        custom_type: str,
        content: JsonValue,
        display: bool,
        role: Literal["system", "user", "assistant"] = "system",
        details: dict[str, JsonValue] | None = None,
    ) -> str:
        """追加参与上下文的自定义消息。"""

        entry: dict[str, object] = {
            "type": "custom_message",
            "customType": custom_type,
            "content": content,
            "display": display,
            "role": role,
        }
        if details is not None:
            entry["details"] = details
        return self._append_entry(entry)

    def branch_with_summary(
        self,
        *,
        entry_id: str,
        summary: str,
        details: dict[str, JsonValue] | None = None,
        from_hook: bool | None = None,
    ) -> str:
        """切换到旧节点，并用 branch_summary 保存离开分支的上下文。"""

        previous_leaf = self._leaf_id
        self.branch(entry_id)
        entry: dict[str, object] = {
            "type": "branch_summary",
            "fromId": previous_leaf or entry_id,
            "summary": summary,
        }
        if details is not None:
            entry["details"] = details
        if from_hook is not None:
            entry["fromHook"] = from_hook
        return self._append_entry(entry)

    def build_session_context(self) -> list[AnyMessage]:
        """从当前 leaf 回溯并重建 LLM 上下文。"""

        branch = self.get_branch()
        compaction = _latest_compaction_entry(branch)

        context: list[AnyMessage] = []
        start_index = 0
        if compaction is not None:
            context.extend(_leading_system_messages(branch))
            summary = str(compaction["summary"])
            context.append(SystemMessage(content=f"Compaction summary:\n{summary}"))
            first_kept_entry_id = str(compaction["firstKeptEntryId"])
            start_index = _find_entry_index(branch, first_kept_entry_id)

        for index, entry in enumerate(branch):
            if compaction is not None and index < start_index:
                continue
            if compaction is not None and entry["id"] == compaction["id"]:
                continue

            entry_type = str(entry["type"])
            if entry_type == "message":
                context.append(_deserialize_message(cast(dict[str, object], entry["message"])))
                continue
            if entry_type == "branch_summary":
                context.append(SystemMessage(content=f"Branch summary:\n{entry['summary']}"))
                continue
            if entry_type == "custom_message":
                role = cast(Literal["system", "user", "assistant"], entry.get("role", "system"))
                context.append(_custom_message_to_langchain(role=role, content=entry["content"]))

        return context

    def _append_entry(self, payload: dict[str, object]) -> str:
        entry_id = _entry_id()
        entry = {
            **payload,
            "id": entry_id,
            "parentId": self._leaf_id,
            "timestamp": _now_iso(),
        }
        with self._session_file.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry, ensure_ascii=False) + "\n")
        self._entries[entry_id] = entry
        self._ordered_ids.append(entry_id)
        self._leaf_id = entry_id
        return entry_id


def _resolve_session_root(storage_root: Path | None) -> Path:
    if storage_root is not None:
        return storage_root.expanduser().resolve()

    env_root = os.getenv(SESSION_ROOT_ENV)
    if env_root:
        return Path(env_root).expanduser().resolve()
    return _DEFAULT_SESSION_ROOT


def _cwd_bucket_name(cwd: Path) -> str:
    return f"--{str(cwd).replace('/', '-')}--"


def _find_session_file(session_dir: Path, session_id: str) -> Path | None:
    for path in sorted(session_dir.glob("*.jsonl")):
        try:
            first_line = path.read_text(encoding="utf-8").splitlines()[0]
        except IndexError:
            continue
        header = cast(dict[str, object], json.loads(first_line))
        if str(header.get("id")) == session_id:
            return path
    return None


def _serialize_message(message: AnyMessage) -> dict[str, JsonValue]:
    if isinstance(message, SystemMessage):
        return {"role": "system", "content": _to_json_value(message.content)}
    if isinstance(message, HumanMessage):
        return {"role": "user", "content": _to_json_value(message.content)}
    if isinstance(message, ToolMessage):
        payload: dict[str, JsonValue] = {
            "role": "tool",
            "content": _to_json_value(message.content),
            "tool_call_id": message.tool_call_id,
        }
        if message.name is not None:
            payload["name"] = message.name
        return payload
    if isinstance(message, AIMessage):
        payload = {
            "role": "assistant",
            "content": _to_json_value(message.content),
            "tool_calls": _to_json_value(message.tool_calls),
            "additional_kwargs": _to_json_value(message.additional_kwargs),
            "response_metadata": _to_json_value(message.response_metadata),
        }
        if message.id is not None:
            payload["message_id"] = message.id
        if message.name is not None:
            payload["name"] = message.name
        return payload
    raise TypeError(f"unsupported message type: {type(message)!r}")


def _deserialize_message(payload: dict[str, object]) -> AnyMessage:
    role = str(payload["role"])
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
        tool_calls = payload.get("tool_calls", [])
        additional_kwargs = payload.get("additional_kwargs", {})
        response_metadata = payload.get("response_metadata", {})
        return AIMessage(
            content=content,
            tool_calls=cast(list[dict[str, object]], tool_calls if isinstance(tool_calls, list) else []),
            additional_kwargs=cast(dict[str, object], additional_kwargs if isinstance(additional_kwargs, dict) else {}),
            response_metadata=cast(dict[str, object], response_metadata if isinstance(response_metadata, dict) else {}),
            id=_optional_str(payload.get("message_id")),
            name=_optional_str(payload.get("name")),
        )
    raise ValueError(f"unsupported message role: {role}")


def _custom_message_to_langchain(*, role: Literal["system", "user", "assistant"], content: object) -> AnyMessage:
    if role == "user":
        return HumanMessage(content=content)
    if role == "assistant":
        return AIMessage(content=content)
    return SystemMessage(content=content)


def _latest_compaction_entry(branch: list[dict[str, object]]) -> dict[str, object] | None:
    for entry in reversed(branch):
        if entry["type"] == "compaction":
            return entry
    return None


def _find_entry_index(branch: list[dict[str, object]], entry_id: str) -> int:
    for index, entry in enumerate(branch):
        if entry["id"] == entry_id:
            return index
    raise ValueError(f"unknown entry on branch: {entry_id}")


def _leading_system_messages(branch: list[dict[str, object]]) -> list[AnyMessage]:
    messages: list[AnyMessage] = []
    for entry in branch:
        if entry["type"] != "message":
            break
        payload = cast(dict[str, object], entry["message"])
        if str(payload.get("role")) != "system":
            break
        messages.append(_deserialize_message(payload))
    return messages


def _to_json_value(value: object) -> JsonValue:
    if value is None or isinstance(value, bool | int | float | str):
        return cast(JsonValue, value)
    if isinstance(value, list):
        return [_to_json_value(item) for item in value]
    if isinstance(value, dict):
        return {str(key): _to_json_value(item) for key, item in value.items()}
    return str(value)


def _optional_str(value: object) -> str | None:
    if isinstance(value, str):
        return value
    return None


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _timestamp_slug(timestamp: str) -> str:
    return timestamp.replace(":", "").replace(".", "").replace("+00:00", "Z")


def _entry_id() -> str:
    return uuid.uuid4().hex[:8]
