"""Entrypoint for Agentland local MCP server."""

from __future__ import annotations

import argparse
import os
from typing import Sequence

from .server import create_server


def serve_mcp(*, transport: str, base_url: str, timeout: int) -> None:
    mcp = create_server(base_url=base_url, timeout=timeout)
    if transport == "streamable-http":
        mcp.run(transport="streamable-http")
        return
    mcp.run(transport="stdio")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Agentland MCP server")
    parser.add_argument(
        "--transport",
        choices=("stdio", "streamable-http"),
        default="stdio",
        help="MCP transport type.",
    )
    parser.add_argument(
        "--base-url",
        default=os.getenv("AGENTLAND_BASE_URL", "http://127.0.0.1:8080"),
        help="Gateway base URL.",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=int(os.getenv("AGENTLAND_TIMEOUT", "30")),
        help="HTTP request timeout in seconds.",
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

