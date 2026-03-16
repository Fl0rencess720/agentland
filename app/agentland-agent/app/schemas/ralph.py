from __future__ import annotations

"""Ralph stream request schema."""

import os

from pydantic import BaseModel, Field


class RalphStreamRequest(BaseModel):
    """Request body for starting or resuming a Ralph-style run."""

    requirement: str = Field(min_length=1)
    workspace_path: str | None = None
    session_id: str | None = None
    project_name: str | None = None
    model: str = Field(default_factory=lambda: os.getenv("OPENAI_MODEL", "gpt-5.2-codex"))
    base_url: str | None = Field(default_factory=lambda: os.getenv("OPENAI_BASE_URL"))
    timeout: float = 60.0
    agent_max_turns: int = 25
    iterations: int = 10
