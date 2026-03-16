from __future__ import annotations

"""会话控制端点（steer/follow-up）。"""

from fastapi import APIRouter

from app.schemas.chat import QueueMessageRequest
from app.services.chat_service import queue_followup, queue_steering

router = APIRouter()


@router.post("/v1/sessions/{session_id}/steer")
async def steer(session_id: str, request: QueueMessageRequest) -> dict[str, object]:
    """向会话的 steering 队列写入消息。"""

    return queue_steering(session_id=session_id, message=request.message)


@router.post("/v1/sessions/{session_id}/followup")
async def followup(session_id: str, request: QueueMessageRequest) -> dict[str, object]:
    """向会话的 follow-up 队列写入消息。"""

    return queue_followup(session_id=session_id, message=request.message)

