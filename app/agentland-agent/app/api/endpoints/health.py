from __future__ import annotations

"""健康检查端点。"""

from fastapi import APIRouter

router = APIRouter()


@router.get("/health")
async def health() -> dict[str, str]:
    """返回服务健康状态。"""

    return {"status": "ok"}

