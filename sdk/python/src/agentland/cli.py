"""Command entrypoint for agentland."""

from __future__ import annotations

import argparse
from typing import Sequence

from ._mcp_args import DEFAULT_TIMEOUT_SECONDS, add_mcp_arguments, env_base_url


def _run_mcp(*, transport: str, base_url: str, timeout: int) -> None:
    from .mcp.__main__ import serve_mcp

    serve_mcp(transport=transport, base_url=base_url, timeout=timeout)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agentland", description="Agentland CLI")
    subparsers = parser.add_subparsers(dest="command")

    mcp_parser = subparsers.add_parser("mcp", help="Run local MCP server")
    add_mcp_arguments(
        mcp_parser,
        default_base_url=env_base_url(),
        default_timeout=DEFAULT_TIMEOUT_SECONDS,
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
