#!/usr/bin/env python3
import json
import os
import pathlib
import pty
import select
import stat
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
BINARY = ROOT / "agentcell-client/dist/agentcell_0.0.0-test_darwin_arm64"
TOKEN = "unique-token-Z7x9-never-print"


def run(args, *, env=None, data=b""):
    merged = os.environ.copy()
    merged.update(env or {})
    return subprocess.run([str(BINARY), *args], input=data, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, env=merged, check=False)


def assert_absent(result, secret=TOKEN):
    streams = result.stdout + result.stderr
    assert secret.encode() not in streams, streams.decode(errors="replace")


def tty_run(args, env=None):
    pid, fd = pty.fork()
    if pid == 0:
        os.environ.update(env or {"AGENTCELL_TOKEN": TOKEN})
        os.execv(str(BINARY), [str(BINARY), *args])
    chunks = []
    while True:
        ready, _, _ = select.select([fd], [], [], 2)
        if not ready:
            break
        try:
            chunk = os.read(fd, 4096)
        except OSError:
            break
        if not chunk:
            break
        chunks.append(chunk)
    _, status = os.waitpid(pid, 0)
    return os.waitstatus_to_exitcode(status), b"".join(chunks)


def check_mcp():
    calls = (
        b'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n'
        b'{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}\n'
    )
    result = run(["mcp"], env={"AGENTCELL_TOKEN": TOKEN}, data=calls)
    assert result.returncode == 0, result.stderr
    assert_absent(result)
    replies = [json.loads(line) for line in result.stdout.splitlines()]
    assert replies[0]["result"]["protocolVersion"] == "2025-06-18"
    tools = replies[1]["result"]["tools"]
    names = [tool["name"] for tool in tools]
    assert all(tool["inputSchema"]["type"] == "object" for tool in tools)
    declared = ["deploy", "logs", "rollback", "env", "secrets", "domains",
                "share", "access", "ps", "spend", "destroy"]
    assert names == declared, names
    print(f"PASS binary MCP initialize + tools/list: {len(tools)} tools with object schemas")
    requested = set(declared + ["reflect"])
    missing = sorted(requested - set(names))
    print(f"FAIL requested 12-verb inventory: missing={missing}; observed={names}")


def check_output_modes():
    base_env = {"AGENTCELL_TOKEN": TOKEN}
    non_tty = run(["unknown-verb"], env=base_env)
    assert non_tty.returncode == 2
    assert json.loads(non_tty.stderr)["code"] == "usage"
    assert_absent(non_tty)
    human_override = run(["--output=human", "unknown-verb"], env=base_env)
    assert human_override.returncode == 2 and human_override.stderr.startswith(b"usage:")
    json_override_tty_code, json_override_tty = tty_run(["--output=json", "unknown-verb"])
    assert json_override_tty_code == 2
    assert json.loads(json_override_tty.strip())["code"] == "usage"
    tty_code, tty_output = tty_run(["unknown-verb"])
    assert tty_code == 2 and tty_output.startswith(b"usage:")
    assert TOKEN.encode() not in tty_output + json_override_tty
    print("PASS binary output selection: pipe=>JSON, TTY=>human, both explicit overrides win")

    with tempfile.TemporaryDirectory() as config:
        no_token_env = {"AGENTCELL_TOKEN": "", "XDG_CONFIG_HOME": config}
        startup_code, startup_output = tty_run(["ps"], no_token_env)
        override_code, override_output = tty_run(["--output=human", "ps"], no_token_env)
        assert startup_code == 10 and override_code == 10
        assert json.loads(startup_output.strip())["code"] == "unauthenticated"
        assert json.loads(override_output.strip())["code"] == "unauthenticated"
        print("FAIL README output claim for startup errors: TTY and --output=human both emitted JSON")


def check_token_storage_and_non_disclosure():
    with tempfile.TemporaryDirectory() as config:
        stored = run(["auth", "token"], env={"XDG_CONFIG_HOME": config, "AGENTCELL_TOKEN": ""}, data=(TOKEN + "\n").encode())
        assert stored.returncode == 0, stored.stderr
        assert stored.stdout == b""
        assert_absent(stored)
        token_path = pathlib.Path(config) / "agentcell/token"
        assert token_path.read_text().strip() == TOKEN
        assert stat.S_IMODE(token_path.stat().st_mode) == 0o600
        assert stat.S_IMODE(token_path.parent.stat().st_mode) == 0o700

        # Re-run the compiled command with identical content after deliberately weakening both modes.
        os.chmod(token_path, 0o644)
        os.chmod(token_path.parent, 0o755)
        restored = run(["auth", "token"], env={"XDG_CONFIG_HOME": config, "AGENTCELL_TOKEN": ""}, data=(TOKEN + "\n").encode())
        assert restored.returncode == 0, restored.stderr
        assert_absent(restored)
        assert stat.S_IMODE(token_path.stat().st_mode) == 0o600
        assert stat.S_IMODE(token_path.parent.stat().st_mode) == 0o700

        argument = run(["auth", "token", TOKEN], env={"XDG_CONFIG_HOME": config + "-empty", "AGENTCELL_TOKEN": TOKEN})
        assert argument.returncode != 0
        assert_absent(argument)

        failed = run(["--api-url=http://127.0.0.1:1", "--output=json", "ps"], env={"XDG_CONFIG_HOME": config, "AGENTCELL_TOKEN": TOKEN})
        assert failed.returncode == 20, (failed.returncode, failed.stderr)
        assert json.loads(failed.stderr)["code"] == "transport"
        assert_absent(failed)
    print("PASS binary token checks: stdin only, 0700/0600, absent from stdout/stderr and transport error")


def check_destroy_confirmation():
    common = ["--api-url=http://127.0.0.1:1", "--output=json", "destroy", "--cell", "Cell-A"]
    near_misses = [["--confirm", ""], ["--confirm", "other"], ["--confirm", "Cell-A "], ["--confirm", "cell-a"]]
    for suffix in near_misses:
        result = run(common + suffix, env={"AGENTCELL_TOKEN": TOKEN})
        assert result.returncode in (2, 14), (suffix, result.returncode, result.stderr)
        error = json.loads(result.stderr)
        assert error["code"] in ("usage", "invalid_request"), error
        assert_absent(result)
    correct = run(common + ["--confirm", "Cell-A"], env={"AGENTCELL_TOKEN": TOKEN})
    assert correct.returncode == 20, (correct.returncode, correct.stderr)
    assert json.loads(correct.stderr)["code"] == "transport"
    assert_absent(correct)
    print("PASS binary destroy validation: four near-misses rejected; exact match alone reached transport")


if __name__ == "__main__":
    assert BINARY.exists(), BINARY
    check_mcp()
    check_output_modes()
    check_token_storage_and_non_disclosure()
    check_destroy_confirmation()
