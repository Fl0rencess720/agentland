"""Entrypoint for Agentland local MCP server."""

from __future__ import annotations

import argparse
from typing import Sequence

from .._mcp_args import add_mcp_arguments, env_base_url, env_timeout
from .server import create_server


def serve_mcp(*, transport: str, base_url: str, timeout: int) -> None:
    mcp = create_server(base_url=base_url, timeout=timeout)
    if transport == "streamable-http":
        mcp.run(transport="streamable-http")
        return
    mcp.run(transport="stdio")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Agentland MCP server")
    add_mcp_arguments(
        parser,
        default_base_url=env_base_url(),
        default_timeout=env_timeout(),
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    serve_mcp(
        transport=args.transport,
        base_url=args.base_url,
        timeout=args.timeout,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
