from __future__ import annotations

"""MCP server 配置。"""

import json
import os
import re
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, Field, ValidationError, model_validator

type JsonValue = None | bool | int | float | str | list["JsonValue"] | dict[str, "JsonValue"]

MCP_CONFIG_ENV = "AGENTLAND_AGENT_MCP_CONFIG"
MCP_CONFIG_PATH_ENV = "AGENTLAND_AGENT_MCP_CONFIG_PATH"


class McpServerConfig(BaseModel):
    """单个 MCP server 的连接配置。"""

    transport: Literal["stdio", "streamable_http", "sse"]
    command: str | None = None
    args: list[str] = Field(default_factory=list)
    url: str | None = None
    headers: dict[str, str] = Field(default_factory=dict)
    env: dict[str, str] = Field(default_factory=dict)
    cwd: str | None = None
    encoding: str | None = None
    encoding_error_handler: str | None = None
    timeout: float | None = None
    sse_read_timeout: float | None = None
    session_kwargs: dict[str, JsonValue] = Field(default_factory=dict)
    enabled: bool = True

    @model_validator(mode="after")
    def validate_transport_fields(self) -> "McpServerConfig":
        if self.transport == "stdio" and not self.command:
            raise ValueError("stdio transport requires command")
        if self.transport in {"streamable_http", "sse"} and not self.url:
            raise ValueError(f"{self.transport} transport requires url")
        return self

    def to_client_dict(self) -> dict[str, JsonValue]:
        """转换为 MultiServerMCPClient 可接受的 dict。"""

        payload = self.model_dump(exclude_none=True, exclude={"enabled"}, mode="json")
        return {key: _expand_json_value(value) for key, value in payload.items()}


# 在这里直接写死需要接入的 MCP 服务即可。
# 例如：
# DEFAULT_MCP_SERVER_CONFIGS = {
#     "filesystem": McpServerConfig(
#         transport="stdio",
#         command="uvx",
#         args=["mcp-server-filesystem", "/absolute/path/to/workspace"],
#     ),
#     "weather": McpServerConfig(
#         transport="streamable_http",
#         url="http://127.0.0.1:8001/mcp",
#         headers={"Authorization": f"Bearer {os.getenv('WEATHER_API_KEY', '')}"},
#     ),
# }
DEFAULT_MCP_SERVER_CONFIGS: dict[str, McpServerConfig] = {}


def load_mcp_server_configs() -> dict[str, dict[str, JsonValue]]:
    """从代码常量和环境变量加载 MCP server 配置。"""

    merged: dict[str, McpServerConfig] = dict(DEFAULT_MCP_SERVER_CONFIGS)

    raw_path = os.getenv(MCP_CONFIG_PATH_ENV)
    if raw_path:
        merged.update(_parse_config_mapping(Path(raw_path).expanduser().resolve().read_text(encoding="utf-8")))

    raw_inline = os.getenv(MCP_CONFIG_ENV)
    if raw_inline:
        merged.update(_parse_config_mapping(raw_inline))

    return {
        name: config.to_client_dict()
        for name, config in merged.items()
        if config.enabled
    }


def _parse_config_mapping(raw: str) -> dict[str, McpServerConfig]:
    document = json.loads(raw)
    if not isinstance(document, dict):
        raise ValueError("MCP config must be a JSON object keyed by server name")

    parsed: dict[str, McpServerConfig] = {}
    for name, value in document.items():
        if not isinstance(name, str):
            raise ValueError("MCP config keys must be strings")
        if not isinstance(value, dict):
            raise ValueError(f"MCP config for {name!r} must be an object")
        try:
            parsed[name] = McpServerConfig.model_validate(value)
        except ValidationError as exc:
            raise ValueError(f"invalid MCP config for {name!r}: {exc}") from exc
    return parsed


def _expand_json_value(value: JsonValue) -> JsonValue:
    if isinstance(value, str):
        return _expand_env_vars(value)
    if isinstance(value, list):
        return [_expand_json_value(item) for item in value]
    if isinstance(value, dict):
        return {key: _expand_json_value(item) for key, item in value.items()}
    return value


def _expand_env_vars(value: str) -> str:
    return re.sub(r"\$\{([^}]+)\}", lambda match: os.getenv(match.group(1), ""), value)
