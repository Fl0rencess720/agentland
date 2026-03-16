from __future__ import annotations

"""聊天相关请求模型。"""

import os
from typing import Literal

from pydantic import BaseModel, Field

DEFAULT_SYSTEM_PROMPT = "You are a helpful assistant. Use tools when needed."


class ChatStreamRequest(BaseModel):
    """单次流式会话请求体。"""

    message: str = Field(min_length=1)
    deep: bool = False
    session_id: str | None = None
    workspace_path: str | None = None
    project_name: str | None = None
    system: str = DEFAULT_SYSTEM_PROMPT
    model: str = Field(default_factory=lambda: os.getenv("OPENAI_MODEL", "gpt-5.2-codex"))
    base_url: str | None = Field(default_factory=lambda: os.getenv("OPENAI_BASE_URL"))
    timeout: float = 60.0
    max_turns: int = 25
    agent_max_turns: int = 25
    iterations: int = 10
    queue_mode: Literal["one-at-a-time", "all"] = "one-at-a-time"


class QueueMessageRequest(BaseModel):
    """steering/follow-up 入队请求体。"""

    message: str = Field(min_length=1)
