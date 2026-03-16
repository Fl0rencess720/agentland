from __future__ import annotations

"""FastAPI 应用主入口。"""

from fastapi import FastAPI

from app.api import api_router


def create_app() -> FastAPI:
    """创建并装配 FastAPI 应用。"""

    application = FastAPI(title="LangGraph Coding Agent SSE Service")
    application.include_router(api_router)
    return application


app = create_app()

