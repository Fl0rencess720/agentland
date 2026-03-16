from __future__ import annotations

"""统一 chat 接口的极简路由层。"""

import json
import threading
from typing import Literal

from langchain_core.messages import AIMessage, AnyMessage, HumanMessage, SystemMessage
from langchain_openai import ChatOpenAI


_router_lock = threading.Lock()
_router_cache: dict[tuple[str, str | None, float], object] = {}


def route_prompt(
    *,
    messages: list[AnyMessage],
    api_key: str,
    model: str,
    base_url: str | None,
    timeout: float,
) -> Literal["chat", "task"]:
    """根据历史对话与当前用户消息，直接返回路由 intent。"""

    return _invoke_router_model(
        messages=messages,
        api_key=api_key,
        model=model,
        base_url=base_url,
        timeout=timeout,
    )


def _invoke_router_model(
    *,
    messages: list[AnyMessage],
    api_key: str,
    model: str,
    base_url: str | None,
    timeout: float,
) -> Literal["chat", "task"]:
    router = _get_router_model(model=model, api_key=api_key, base_url=base_url, timeout=timeout)
    content = _collect_streamed_content(
        router,
        [
            SystemMessage(
                content=(
                    "You are a router for a coding agent.\n"
                    "Read the conversation history and classify the latest user intent.\n"
                    "Return intent='chat' when the user mainly wants an answer, explanation, discussion, or advice.\n"
                    "Return intent='task' when the user wants the agent to execute a multi-step task, modify files, run tools, or operate on a workspace.\n"
                    'Reply with JSON only in this exact shape: {"intent":"chat|task","reason":"string"}'
                )
            ),
            *_normalize_router_messages(messages),
        ],
    )
    payload = json.loads(content)
    return payload["intent"]


def _get_router_model(*, model: str, api_key: str, base_url: str | None, timeout: float):
    cache_key = (model, base_url, timeout)
    with _router_lock:
        router = _router_cache.get(cache_key)
        if router is not None:
            return router

        router = ChatOpenAI(
            model=model,
            api_key=api_key,
            base_url=base_url,
            timeout=timeout,
            streaming=True,
            max_retries=1,
            use_responses_api=False,
        )
        _router_cache[cache_key] = router
        return router


def _normalize_router_messages(messages: list[AnyMessage]) -> list[AnyMessage]:
    """路由仅保留会影响意图判断的用户/助手文本消息。"""

    normalized: list[AnyMessage] = []
    for message in messages:
        if isinstance(message, HumanMessage):
            content = _extract_text_content(message.content)
            if content:
                normalized.append(HumanMessage(content=content))
            continue
        if isinstance(message, AIMessage):
            content = _extract_text_content(message.content)
            if content:
                normalized.append(AIMessage(content=content))
    return normalized


def _collect_streamed_content(model: object, messages: list[AnyMessage]) -> str:
    chunks: list[object] = []
    for chunk in model.stream(messages):
        chunks.append(chunk)

    if not chunks:
        raise RuntimeError("router stream returned no chunks")

    merged = chunks[0]
    for chunk in chunks[1:]:
        merged = merged + chunk

    return _extract_text_content(getattr(merged, "content", "")).strip()


def _extract_text_content(content: object) -> str:
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""

    parts: list[str] = []
    for item in content:
        if isinstance(item, str):
            parts.append(item)
            continue
        if isinstance(item, dict):
            text = item.get("text")
            if isinstance(text, str):
                parts.append(text)
                continue
            nested = item.get("content")
            if isinstance(nested, str):
                parts.append(nested)
                continue
        text_attr = getattr(item, "text", None)
        if isinstance(text_attr, str):
            parts.append(text_attr)
            continue
        content_attr = getattr(item, "content", None)
        if isinstance(content_attr, str):
            parts.append(content_attr)
    return "".join(parts).strip()
