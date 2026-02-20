#!/usr/bin/env python3
import argparse
import json
import time
from queue import Empty

from jupyter_client import BlockingKernelClient


def _build_client(connection_file: str) -> BlockingKernelClient:
    client = BlockingKernelClient(connection_file=connection_file)
    client.load_connection_file(connection_file)
    return client


def _emit(payload: dict) -> int:
    print(json.dumps(payload, ensure_ascii=False))
    return 0


def probe(args: argparse.Namespace) -> int:
    client = _build_client(args.connection_file)
    try:
        client.start_channels()
        client.wait_for_ready(timeout=args.timeout_ms / 1000.0)
        return _emit({"ok": True})
    finally:
        try:
            client.stop_channels()
        except Exception:
            pass


def execute(args: argparse.Namespace) -> int:
    with open(args.code_file, "r", encoding="utf-8") as f:
        code = f.read()

    timeout_seconds = args.timeout_ms / 1000.0
    deadline = time.monotonic() + timeout_seconds

    client = _build_client(args.connection_file)
    stdout_chunks = []
    stderr_chunks = []
    execution_count = 0
    status = "ok"

    def remaining() -> float:
        return max(0.0, deadline - time.monotonic())

    try:
        client.start_channels()
        client.wait_for_ready(timeout=min(5.0, timeout_seconds))
        msg_id = client.execute(
            code,
            silent=False,
            store_history=True,
            allow_stdin=False,
            stop_on_error=False,
        )

        while True:
            rem = remaining()
            if rem <= 0:
                status = "timeout"
                break
            try:
                msg = client.get_iopub_msg(timeout=rem)
            except Empty:
                status = "timeout"
                break

            parent = msg.get("parent_header", {})
            if parent.get("msg_id") != msg_id:
                continue

            msg_type = msg.get("msg_type", "")
            content = msg.get("content", {})
            if msg_type == "stream":
                if content.get("name") == "stderr":
                    stderr_chunks.append(content.get("text", ""))
                else:
                    stdout_chunks.append(content.get("text", ""))
            elif msg_type == "error":
                tb = content.get("traceback") or []
                if tb:
                    stderr_chunks.append("\n".join(tb) + "\n")
                elif content.get("evalue"):
                    stderr_chunks.append(str(content.get("evalue")) + "\n")
            elif msg_type == "execute_input":
                execution_count = int(content.get("execution_count", execution_count))
            elif msg_type == "status" and content.get("execution_state") == "idle":
                break

        shell_status = "ok"
        while status != "timeout":
            rem = remaining()
            if rem <= 0:
                status = "timeout"
                break
            try:
                reply = client.get_shell_msg(timeout=rem)
            except Empty:
                status = "timeout"
                break
            parent = reply.get("parent_header", {})
            if parent.get("msg_id") != msg_id:
                continue
            content = reply.get("content", {})
            shell_status = content.get("status", "ok")
            execution_count = int(content.get("execution_count", execution_count))
            if shell_status == "error":
                tb = content.get("traceback") or []
                if tb:
                    stderr_chunks.append("\n".join(tb) + "\n")
                elif content.get("evalue"):
                    stderr_chunks.append(str(content.get("evalue")) + "\n")
            break

        if status == "timeout":
            try:
                client.interrupt_kernel()
            except Exception:
                pass
            stderr_chunks.append(
                f"Execution timed out after {args.timeout_ms} ms.\n"
            )
        else:
            status = shell_status

        return _emit(
            {
                "status": status,
                "execution_count": execution_count,
                "stdout": "".join(stdout_chunks),
                "stderr": "".join(stderr_chunks),
            }
        )
    finally:
        try:
            client.stop_channels()
        except Exception:
            pass


def shutdown(args: argparse.Namespace) -> int:
    client = _build_client(args.connection_file)
    try:
        client.start_channels()
        try:
            client.wait_for_ready(timeout=min(3.0, args.timeout_ms / 1000.0))
        except Exception:
            pass
        try:
            client.shutdown(restart=False)
        except Exception:
            pass
        return _emit({"ok": True})
    finally:
        try:
            client.stop_channels()
        except Exception:
            pass


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)

    p_probe = sub.add_parser("probe")
    p_probe.add_argument("--connection-file", required=True)
    p_probe.add_argument("--timeout-ms", type=int, default=5000)
    p_probe.set_defaults(func=probe)

    p_exec = sub.add_parser("execute")
    p_exec.add_argument("--connection-file", required=True)
    p_exec.add_argument("--code-file", required=True)
    p_exec.add_argument("--timeout-ms", type=int, default=30000)
    p_exec.set_defaults(func=execute)

    p_shutdown = sub.add_parser("shutdown")
    p_shutdown.add_argument("--connection-file", required=True)
    p_shutdown.add_argument("--timeout-ms", type=int, default=5000)
    p_shutdown.set_defaults(func=shutdown)

    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
