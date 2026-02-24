from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from agentland import cli


class CLITests(unittest.TestCase):
    @mock.patch("agentland.cli._run_mcp")
    def test_agentland_mcp_invokes_runner(self, run_mcp: mock.Mock) -> None:
        rc = cli.main(
            [
                "mcp",
                "--transport",
                "stdio",
                "--base-url",
                "http://127.0.0.1:18080",
                "--timeout",
                "40",
            ]
        )
        self.assertEqual(0, rc)
        run_mcp.assert_called_once_with(
            transport="stdio",
            base_url="http://127.0.0.1:18080",
            timeout=40,
        )


if __name__ == "__main__":
    unittest.main()

