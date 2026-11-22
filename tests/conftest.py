import json

import pytest

from fakehike import FakeHike
from hikecli.cli import main


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
    monkeypatch.setenv("HIKECLI_STORE", str(path))
    monkeypatch.delenv("HIKECLI_READONLY", raising=False)
    return path


class CLI:
    def __init__(self, capsys):
        self.capsys = capsys

    def __call__(self, *args):
        """Run `hikecli --json ...` in-process; returns (exit code, JSON envelope, stderr)."""
        code = main(["--json", *args])
        captured = self.capsys.readouterr()
        lines = [line for line in captured.out.splitlines() if line.strip()]
        return code, (json.loads(lines[-1]) if lines else None), captured.err


@pytest.fixture
def cli(store, capsys):
    return CLI(capsys)


@pytest.fixture
def logged_in(cli):
    code, out, err = cli("auth", "--batch", "5")
    assert code == 0, err
    return out["data"]
