"""Command entrypoint for agentland."""

from __future__ import annotations

import argparse
from typing import Sequence


def _run_mcp(*, transport: str, base_url: str, timeout: int) -> None:
    from .mcp.__main__ import serve_mcp

    serve_mcp(transport=transport, base_url=base_url, timeout=timeout)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agentland", description="Agentland CLI")
    subparsers = parser.add_subparsers(dest="command")

    mcp_parser = subparsers.add_parser("mcp", help="Run local MCP server")
    mcp_parser.add_argument(
        "--transport",
        choices=("stdio", "streamable-http"),
        default="stdio",
        help="MCP transport type.",
    )
    mcp_parser.add_argument(
        "--base-url",
        default="http://127.0.0.1:8080",
        help="Gateway base URL.",
    )
    mcp_parser.add_argument(
        "--timeout",
        type=int,
        default=30,
        help="HTTP request timeout in seconds.",
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.command == "mcp":
        _run_mcp(
            transport=args.transport,
            base_url=args.base_url,
            timeout=args.timeout,
        )
        return 0

    parser.print_help()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

