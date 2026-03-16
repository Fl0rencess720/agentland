from __future__ import annotations

"""MCP tool loading tests."""

from pathlib import Path

from app.services.mcp_config import load_mcp_server_configs
from app.services.tools import clear_tool_cache, load_tools, tool_signature


def test_load_mcp_server_configs_from_env_path(monkeypatch, tmp_path: Path) -> None:  # noqa: ANN001
    """MCP config should load from JSON file and expand env vars."""

    config_path = tmp_path / "mcp.json"
    monkeypatch.setenv("MCP_TOKEN", "secret-token")
    monkeypatch.setenv("AGENTLAND_AGENT_MCP_CONFIG_PATH", str(config_path))
    config_path.write_text(
        """
        {
          "weather": {
            "transport": "streamable_http",
            "url": "http://127.0.0.1:9000/mcp",
            "headers": {
              "Authorization": "Bearer ${MCP_TOKEN}"
            }
          }
        }
        """.strip(),
        encoding="utf-8",
    )

    configs = load_mcp_server_configs()
    assert configs == {
        "weather": {
            "transport": "streamable_http",
            "url": "http://127.0.0.1:9000/mcp",
            "headers": {"Authorization": "Bearer secret-token"},
            "args": [],
            "env": {},
            "session_kwargs": {},
        }
    }


def test_load_tools_merges_builtin_and_mcp_tools(monkeypatch) -> None:  # noqa: ANN001
    """Configured MCP tools should be merged into the builtin tool list."""

    captured: dict[str, object] = {}

    class FakeMcpTool:
        def __init__(self, name: str) -> None:
            self.name = name

    class FakeClient:
        def __init__(self, servers: dict[str, object]) -> None:
            captured["servers"] = servers

        async def get_tools(self) -> list[object]:
            return [FakeMcpTool("weather_lookup"), FakeMcpTool("math_add")]

    monkeypatch.setattr(
        "app.services.tools.load_mcp_server_configs",
        lambda: {
            "weather": {
                "transport": "streamable_http",
                "url": "http://127.0.0.1:9000/mcp",
            }
        },
    )
    monkeypatch.setattr("app.services.tools.MultiServerMCPClient", FakeClient)
    clear_tool_cache()

    tools = load_tools()

    assert captured["servers"] == {
        "weather": {
            "transport": "streamable_http",
            "url": "http://127.0.0.1:9000/mcp",
        }
    }
    assert tool_signature(tools) == ("bash", "read", "write", "weather_lookup", "math_add")
