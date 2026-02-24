"""Shared MCP CLI argument helpers."""

from __future__ import annotations

import argparse
import os

DEFAULT_BASE_URL = "http://127.0.0.1:8080"
DEFAULT_TIMEOUT_SECONDS = 30


def env_base_url() -> str:
    return os.getenv("AGENTLAND_BASE_URL", DEFAULT_BASE_URL)


def env_timeout() -> int:
    raw = os.getenv("AGENTLAND_TIMEOUT")
    if raw is None:
        return DEFAULT_TIMEOUT_SECONDS
    text = raw.strip()
    if not text:
        return DEFAULT_TIMEOUT_SECONDS
    try:
        return int(text)
    except ValueError:
        return DEFAULT_TIMEOUT_SECONDS


def add_mcp_arguments(
    parser: argparse.ArgumentParser,
    *,
    default_base_url: str,
    default_timeout: int,
) -> None:
    parser.add_argument(
        "--transport",
        choices=("stdio", "streamable-http"),
        default="stdio",
        help="MCP transport type.",
    )
    parser.add_argument(
        "--base-url",
        default=default_base_url,
        help="Gateway base URL.",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=default_timeout,
        help="HTTP request timeout in seconds.",
    )
