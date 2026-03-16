from __future__ import annotations

"""Ralph orchestration domain models."""

from pathlib import Path
from threading import Lock

from pydantic import BaseModel, Field


class RalphUserStory(BaseModel):
    """A single Ralph user story item persisted in `prd.json`."""

    id: str = Field(min_length=1)
    title: str = Field(min_length=1)
    description: str = Field(min_length=1)
    acceptanceCriteria: list[str] = Field(min_length=1)
    priority: int = Field(ge=1)
    passes: bool = False
    notes: str = ""


class RalphPrd(BaseModel):
    """Structured Ralph plan stored on disk between iterations."""

    project: str = Field(min_length=1)
    branchName: str = Field(min_length=1)
    description: str = Field(min_length=1)
    userStories: list[RalphUserStory] = Field(min_length=1)

    def sorted_stories(self) -> list[RalphUserStory]:
        """Return stories ordered by priority and id."""

        return sorted(self.userStories, key=lambda story: (story.priority, story.id))


class RalphRunState(BaseModel):
    """In-memory metadata for a Ralph run."""

    session_id: str
    run_id: str
    workspace_path: Path
    session_root: Path
    run_dir: Path
    prd_path: Path
    progress_path: Path

    model_config = {"arbitrary_types_allowed": True}


class RalphRunLock:
    """Concurrency guard for a Ralph chat session."""

    __slots__ = ("session_id", "lock", "running")

    def __init__(self, session_id: str) -> None:
        self.session_id = session_id
        self.lock = Lock()
        self.running = False
