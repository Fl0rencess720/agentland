from __future__ import annotations

"""会话状态模型。"""

import queue
import threading
from pathlib import Path
from typing import TYPE_CHECKING
from dataclasses import dataclass, field

from langchain_core.messages import AnyMessage

if TYPE_CHECKING:
    from app.services.session_memory import SessionManager


@dataclass(slots=True)
class SessionState:
    """活跃会话运行态及会话级消息队列。"""

    session_id: str
    workspace_path: Path
    manager: SessionManager
    steering_queue: queue.Queue[AnyMessage] = field(default_factory=lambda: queue.Queue(maxsize=64))
    followup_queue: queue.Queue[AnyMessage] = field(default_factory=lambda: queue.Queue(maxsize=64))
    lock: threading.Lock = field(default_factory=threading.Lock)
    running: bool = False
