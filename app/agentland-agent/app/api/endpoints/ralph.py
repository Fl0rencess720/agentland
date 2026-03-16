from __future__ import annotations

"""Ralph-compatible stream endpoint."""

from fastapi import APIRouter
from fastapi.responses import StreamingResponse

from app.schemas.ralph import RalphStreamRequest
from app.services.ralph_service import stream_ralph

router = APIRouter()


@router.post("/v1/ralph/stream")
async def ralph_stream(request: RalphStreamRequest) -> StreamingResponse:
    """Start or resume a Ralph-style orchestration loop."""

    return await stream_ralph(request)
