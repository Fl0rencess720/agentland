from __future__ import annotations

"""基于 LangGraph 原语实现的核心 Agent 循环。

本模块对齐 Go demo 语义：
- LLM -> tools -> LLM 循环
- steering 消息可在工具执行阶段打断
- follow-up 消息会在原本停止后继续执行
"""

from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from typing import Literal, TypedDict

from langchain_core.messages import AIMessage, AnyMessage, ToolMessage
from langgraph.graph import END, START, StateGraph
from langgraph.prebuilt import ToolNode


@dataclass(slots=True)
class Hooks:
    """可选生命周期回调，供 UI/传输层订阅。"""

    on_turn_start: Callable[[int], None] | None = None
    on_assistant_delta: Callable[[str], None] | None = None
    on_assistant: Callable[[AIMessage], None] | None = None
    on_tool_call: Callable[[dict], None] | None = None
    on_tool_result: Callable[[ToolMessage], None] | None = None
    on_turn_end: Callable[[int], None] | None = None


@dataclass(slots=True)
class AgentConfig:
    """Agent 循环配置。"""

    model: object
    tools: Sequence[object]
    max_turns: int = 25
    transform_context: Callable[[list[AnyMessage]], list[AnyMessage]] | None = None
    get_steering_messages: Callable[[], list[AnyMessage]] | None = None
    get_followup_messages: Callable[[], list[AnyMessage]] | None = None
    hooks: Hooks = field(default_factory=Hooks)


class LoopState(TypedDict):
    """一次图执行的状态结构。"""

    messages: list[AnyMessage]
    turn_count: int
    max_turns: int
    has_tool_calls: bool
    pending_steering: list[AnyMessage]
    active_turn: int


def run_agent(messages: list[AnyMessage], cfg: AgentConfig) -> list[AnyMessage]:
    """运行直到没有工具调用且没有 follow-up 消息。"""

    if cfg.model is None:
        raise ValueError("agent: model is required")
    if not cfg.tools:
        raise ValueError("agent: at least one tool is required")

    tool_node = ToolNode(cfg.tools)
    graph = _build_graph(cfg, tool_node)

    history = list(messages)
    pending = _drain(cfg.get_steering_messages)
    if pending:
        history.extend(pending)

    turn_count = 0
    # 外层循环：仅当存在 follow-up 消息时继续下一轮。
    while True:
        result = graph.invoke(
            {
                "messages": history,
                "turn_count": turn_count,
                "max_turns": cfg.max_turns,
                "has_tool_calls": False,
                "pending_steering": [],
                "active_turn": 0,
            }
        )
        history = result["messages"]
        turn_count = result["turn_count"]

        followups = _drain(cfg.get_followup_messages)
        if not followups:
            break
        history.extend(followups)

    return history


def _build_graph(cfg: AgentConfig, tool_node: ToolNode):
    """构建内层循环图：llm_call -> tool_exec -> llm_call（或结束）。"""

    def llm_call(state: LoopState) -> LoopState:
        turn = state["turn_count"]
        if turn >= state["max_turns"]:
            raise RuntimeError(f"agent: max turns ({state['max_turns']}) reached")

        if cfg.hooks.on_turn_start is not None:
            cfg.hooks.on_turn_start(turn)

        # 每次模型调用前可对消息做变换（例如上下文压缩）。
        messages = state["messages"]
        if cfg.transform_context is not None:
            messages = cfg.transform_context(messages)

        assistant = _stream_to_message(cfg.model, messages, cfg.hooks.on_assistant_delta)
        if cfg.hooks.on_assistant is not None:
            cfg.hooks.on_assistant(assistant)

        has_tool_calls = bool(assistant.tool_calls)
        pending_steering: list[AnyMessage] = []
        if not has_tool_calls:
            pending_steering = _drain(cfg.get_steering_messages)
            if cfg.hooks.on_turn_end is not None:
                cfg.hooks.on_turn_end(turn)

        return {
            "messages": [*messages, assistant],
            "turn_count": turn + 1,
            "max_turns": state["max_turns"],
            "has_tool_calls": has_tool_calls,
            "pending_steering": pending_steering,
            "active_turn": turn,
        }

    def tool_exec(state: LoopState) -> LoopState:
        messages = state["messages"]
        last = messages[-1]
        if not isinstance(last, AIMessage):
            raise RuntimeError("agent: last message is not AIMessage before tool execution")

        tool_calls = list(last.tool_calls or [])
        results: list[ToolMessage] = []
        pending: list[AnyMessage] = []

        # 串行执行 tool call，便于在每次调用后检查 steering。
        for index, call in enumerate(tool_calls):
            if cfg.hooks.on_tool_call is not None:
                cfg.hooks.on_tool_call(call)

            single_call_message = AIMessage(content="", tool_calls=[call])
            try:
                raw = tool_node.invoke({"messages": [single_call_message]})
                tool_messages = _extract_tool_messages(raw)
            except Exception as exc:  # noqa: BLE001
                tool_messages = [
                    ToolMessage(
                        content=f"tool {call.get('name', '')} failed: {exc}",
                        tool_call_id=call.get("id", ""),
                        name=call.get("name"),
                    )
                ]

            for tool_message in tool_messages:
                results.append(tool_message)
                if cfg.hooks.on_tool_result is not None:
                    cfg.hooks.on_tool_result(tool_message)

            # 若检测到 steering，本轮剩余 tool call 直接跳过。
            steering = _drain(cfg.get_steering_messages)
            if steering:
                pending = steering
                for skipped in tool_calls[index + 1 :]:
                    results.append(
                        ToolMessage(
                            content="Skipped due to queued user message.",
                            tool_call_id=skipped.get("id", ""),
                            name=skipped.get("name"),
                        )
                    )
                break

        if cfg.hooks.on_turn_end is not None:
            cfg.hooks.on_turn_end(state["active_turn"])

        return {
            "messages": [*messages, *results],
            "turn_count": state["turn_count"],
            "max_turns": state["max_turns"],
            "has_tool_calls": False,
            "pending_steering": pending,
            "active_turn": state["active_turn"],
        }

    def inject_steering(state: LoopState) -> LoopState:
        return {
            "messages": [*state["messages"], *state["pending_steering"]],
            "turn_count": state["turn_count"],
            "max_turns": state["max_turns"],
            "has_tool_calls": False,
            "pending_steering": [],
            "active_turn": state["active_turn"],
        }

    def route_after_llm(state: LoopState) -> Literal["tool_exec", "inject_steering", END]:
        if state["has_tool_calls"]:
            return "tool_exec"
        if state["pending_steering"]:
            return "inject_steering"
        return END

    def route_after_tools(state: LoopState) -> Literal["inject_steering", "llm_call"]:
        if state["pending_steering"]:
            return "inject_steering"
        return "llm_call"

    builder = StateGraph(LoopState)
    builder.add_node("llm_call", llm_call)
    builder.add_node("tool_exec", tool_exec)
    builder.add_node("inject_steering", inject_steering)
    builder.add_edge(START, "llm_call")
    builder.add_conditional_edges("llm_call", route_after_llm, {"tool_exec": "tool_exec", "inject_steering": "inject_steering", END: END})
    builder.add_conditional_edges("tool_exec", route_after_tools, {"inject_steering": "inject_steering", "llm_call": "llm_call"})
    builder.add_edge("inject_steering", "llm_call")
    return builder.compile()


def _stream_to_message(
    model: object,
    messages: list[AnyMessage],
    on_assistant_delta: Callable[[str], None] | None = None,
) -> AIMessage:
    """流式拉取模型输出并合并为单条 AIMessage。

    对部分仅“半支持”流式的网关，自动回退到非流式 invoke。
    """

    chunks: list[AnyMessage] = []
    try:
        for chunk in model.stream(messages):
            if on_assistant_delta is not None:
                delta_text = _extract_text_content(getattr(chunk, "content", ""))
                if delta_text:
                    on_assistant_delta(delta_text)
            chunks.append(chunk)
    except Exception:
        # 某些 OpenAI 兼容网关流式不完整，回退到 invoke。
        invoked = model.invoke(messages)
        if isinstance(invoked, AIMessage):
            return invoked
        raise
    if not chunks:
        invoked = model.invoke(messages)
        if isinstance(invoked, AIMessage):
            return invoked
        raise RuntimeError("agent: model stream returned no chunks")

    merged = chunks[0]
    for chunk in chunks[1:]:
        merged = merged + chunk

    if isinstance(merged, AIMessage):
        return merged

    if hasattr(merged, "to_message"):
        converted = merged.to_message()
        if isinstance(converted, AIMessage):
            return converted

    content = getattr(merged, "content", "")
    tool_calls = getattr(merged, "tool_calls", [])
    additional_kwargs = getattr(merged, "additional_kwargs", {})
    response_metadata = getattr(merged, "response_metadata", {})
    message_id = getattr(merged, "id", None)
    return AIMessage(
        content=content,
        tool_calls=tool_calls,
        additional_kwargs=additional_kwargs,
        response_metadata=response_metadata,
        id=message_id,
    )


def _extract_text_content(content: object) -> str:
    """从字符串或结构化内容块中提取可展示文本。"""

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


def _extract_tool_messages(raw: object) -> list[ToolMessage]:
    """将 ToolNode 输出统一归一化为 ToolMessage 列表。"""

    if isinstance(raw, dict):
        messages = raw.get("messages", [])
    elif isinstance(raw, list):
        messages = raw
    else:
        raise RuntimeError(f"unexpected ToolNode output type: {type(raw)!r}")

    tool_messages: list[ToolMessage] = []
    for message in messages:
        if isinstance(message, ToolMessage):
            tool_messages.append(message)
    return tool_messages


def _drain(fn: Callable[[], list[AnyMessage]] | None) -> list[AnyMessage]:
    """从回调中取出排队消息；未提供回调则返回空列表。"""

    if fn is None:
        return []
    return fn()

