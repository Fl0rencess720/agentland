from __future__ import annotations

"""FastAPI 入口基础测试。"""

import pytest
from fastapi.testclient import TestClient

pytest.importorskip("langgraph")

from app.main import app


def test_health() -> None:
    """健康检查应返回 200 + ok。"""

    client = TestClient(app)
    response = client.get("/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}
