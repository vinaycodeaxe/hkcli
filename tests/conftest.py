import json
import os
import subprocess
from pathlib import Path

import pytest

from fakehike import FakeHike

ROOT = Path(__file__).resolve().parent.parent
BIN = Path(os.environ.get("HKCLI_BIN") or ROOT / "dist" / ("hkcli.exe" if os.name == "nt" else "hkcli"))


@pytest.fixture
def fake():
    server = FakeHike().start()
    yield server
    server.stop()


@pytest.fixture
def store(fake, tmp_path, monkeypatch):
    for key, value in fake.env().items():
        monkeypatch.setenv(key, value)
    path = tmp_path / "store"
    monkeypatch.setenv("HKCLI_STORE", str(path))
    monkeypatch.delenv("HKCLI_READONLY", raising=False)
    return path


class CLI:
    """Runs the hkcli binary as `hkcli --json ...`."""

    bin = BIN

    def __call__(self, *args, input=None):
        """Returns (exit code, last JSON line of stdout, stderr). `input` is fed to stdin."""
        stdin = {"input": input} if input is not None else {"stdin": subprocess.DEVNULL}
        proc = subprocess.run(
            [str(BIN), "--json", *args],
            capture_output=True, text=True, encoding="utf-8", timeout=120, env=dict(os.environ), **stdin,
        )
        lines = [line for line in proc.stdout.splitlines() if line.strip()]
        return proc.returncode, (json.loads(lines[-1]) if lines else None), proc.stderr


@pytest.fixture
def cli(store):
    if not BIN.is_file():
        pytest.fail(f"hkcli binary not found at {BIN}; build it with: go build -o {BIN} ./cmd/hkcli (or set HKCLI_BIN)")
    return CLI()


@pytest.fixture
def logged_in(cli):
    code, out, err = cli("auth", "--batch", "5")
    assert code == 0, err
    return out["data"]
