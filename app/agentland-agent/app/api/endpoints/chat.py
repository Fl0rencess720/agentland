from __future__ import annotations

"""聊天流式端点。"""

from fastapi import APIRouter
from fastapi.responses import StreamingResponse

from app.schemas.chat import ChatStreamRequest
from app.services.chat_service import stream_chat

router = APIRouter()


@router.post("/v1/chat/stream")
async def chat_stream(request: ChatStreamRequest) -> StreamingResponse:
    """启动一次会话并以 SSE 流式返回事件。"""

    return await stream_chat(request)

