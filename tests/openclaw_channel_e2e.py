"""End-to-end check of the OpenClaw Hike channel plugin against the fake Hike servers (no LLM).

Installs the packed plugin into an isolated OpenClaw state dir, links the agent's Hike account
through `openclaw channels login` (the fake confirms the QR login by itself), runs a real
`openclaw gateway`, pushes a DM from an unknown sender and expects the plugin's pairing reply on
the fake relay. Then approves the request through the Gateway with notify (the Control UI path),
expects the confirmation, stops the Gateway and checks that no hkcli sync is left behind.

Usage: python tests/openclaw_channel_e2e.py --hkcli PATH --plugin PATH.tgz
Needs `openclaw` on PATH, hkcli 0.3.0+ built from this repo, and requirements-dev.txt.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from fakehike import NEW_PEER, PEER, USER, FakeHike  # noqa: E402


def log(message: str) -> None:
    print(f"[e2e] {message}", flush=True)


def run(cmd: list[str], env: dict, timeout: int = 300, check: bool = True) -> subprocess.CompletedProcess:
    log("$ " + " ".join(cmd))
    proc = subprocess.run(cmd, env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout)
    output = (proc.stdout + proc.stderr).strip()
    if output:
        print(output, flush=True)
    if check and proc.returncode != 0:
        raise SystemExit(f"exit code {proc.returncode}: {' '.join(cmd)}")
    return proc


def wait_for(predicate, timeout: float, what: str):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.25)
    raise SystemExit(f"timed out after {timeout:.0f}s waiting for {what}")


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def sent_to(fake: FakeHike, peer: str) -> list[str]:
    """Texts the agent's account published to `peer`."""
    return [(p.get("d") or {}).get("m") or "" for p in fake.packets("m") if p.get("to") == peer]


def dm(sender: str, text: str) -> dict:
    ts = int(time.time() * 1000)
    return {
        "t": "m", "st": "text", "f": sender, "to": USER, "c": sender, "i": str(uuid.uuid4()),
        "s": f"{ts % 100000}:{ts}", "ts": ts, "d": {"st": "text", "ts": ts, "m": text, "p": text},
        "pi": {"n": "Neel", "p": "+919800000009"},
    }


def relay_subscribed(fake: FakeHike) -> bool:
    with fake.lock:
        return any(f"u:{USER}" in s["topics"] for s in fake.sessions)


def hkcli_sync_processes(hkcli: str) -> list[tuple[int, str]]:
    proc = subprocess.run(["pgrep", "-af", hkcli], capture_output=True, text=True)
    found = []
    for line in proc.stdout.splitlines():
        pid, _, cmd = line.partition(" ")
        if cmd.startswith(hkcli) and " sync" in cmd:
            found.append((int(pid), cmd))
    return found


def gateway_call(openclaw: str, method: str, params: dict, env: dict):
    proc = run([openclaw, "gateway", "call", method, "--params", json.dumps(params), "--json"], env, timeout=120)
    text = proc.stdout.strip()
    start = min((i for i in (text.find("{"), text.find("[")) if i >= 0), default=-1)
    if start < 0:
        raise SystemExit(f"{method} returned no JSON")
    return json.loads(text[start:])


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hkcli", required=True)
    parser.add_argument("--plugin", required=True, help="npm pack tarball of openclaw-plugin")
    parser.add_argument("--workdir", default=os.environ.get("RUNNER_TEMP") or tempfile.gettempdir())
    args = parser.parse_args()
    hkcli = str(Path(args.hkcli).resolve())
    plugin = str(Path(args.plugin).resolve())
    openclaw = shutil.which("openclaw") or "openclaw"

    work = Path(tempfile.mkdtemp(prefix="hike-e2e-", dir=args.workdir))
    state, home = work / "state", work / "home"
    state.mkdir()
    home.mkdir()
    fake = FakeHike().start()
    env = {
        **os.environ,
        **fake.env(),
        "HOME": str(home),
        "OPENCLAW_STATE_DIR": str(state),
        "OPENCLAW_GATEWAY_TOKEN": secrets.token_hex(16),
        "NO_COLOR": "1",
    }
    env.pop("HKCLI_STORE", None)
    env.pop("HKCLI_READONLY", None)
    port = free_port()
    gateway = None
    gateway_log = work / "gateway.log"
    failure = None
    try:
        run([hkcli, "--version"], env)
        run([openclaw, "--version"], env)
        run([openclaw, "plugins", "install", f"npm-pack:{plugin}", "--force", "--accept-capabilities"], env)
        run([openclaw, "config", "set", "gateway.mode", "local"], env)
        run([openclaw, "config", "set", "gateway.port", str(port), "--strict-json"], env)
        run([openclaw, "config", "set", "channels.hike.enabled", "true", "--strict-json"], env)
        run([openclaw, "config", "set", "channels.hike.cliPath", hkcli], env)

        run([openclaw, "channels", "login", "--channel", "hike"], env, timeout=180)
        session = state / "hike" / "default" / "session.json"
        if not session.is_file():
            raise SystemExit(f"login did not create {session}")
        log(f"linked; store {session.parent}")

        connects_before = len(fake.mqtt_connects)
        with gateway_log.open("w") as out:
            gateway = subprocess.Popen(
                [openclaw, "gateway", "run", "--port", str(port), "--bind", "loopback", "--verbose"],
                env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT, start_new_session=True,
            )
        wait_for(lambda: gateway.poll() is not None or (len(fake.mqtt_connects) > connects_before and relay_subscribed(fake)),
                 180, "the Gateway's hkcli sync to connect to the fake relay")
        if gateway.poll() is not None:
            raise SystemExit(f"gateway exited early with code {gateway.returncode}")
        log("hkcli sync is connected")
        time.sleep(3)

        fake.push(dm(NEW_PEER, "hello, who is this?"))
        reply = wait_for(lambda: [t for t in sent_to(fake, NEW_PEER) if "Pairing code" in t], 90, "the pairing reply")[0]
        log(f"pairing reply: {reply!r}")
        code = re.search(r"Pairing code: (\S+)", reply).group(1)
        if f"openclaw pairing approve hike {code}" not in reply:
            raise SystemExit("pairing reply does not explain how to approve")
        if sent_to(fake, PEER):
            raise SystemExit(f"answered Hike history from before the first start: {sent_to(fake, PEER)}")

        # The CLI approves by code; the Gateway path (used by the Control UI) can also notify.
        listing = gateway_call(openclaw, "channels.pairing.list", {"channel": "hike"}, env)
        request = next(r for r in listing["requests"] if r["senderId"] == NEW_PEER)
        if not request.get("notifySupported"):
            raise SystemExit(f"pairing request does not support notification: {request}")
        approved = gateway_call(
            openclaw,
            "channels.pairing.approve",
            {"channel": "hike", "accountId": request["accountId"], "requestId": request["requestId"], "notify": True},
            env,
        )
        if approved.get("notification") != "sent":
            raise SystemExit(f"approval notification was not sent: {approved}")
        wait_for(lambda: len(sent_to(fake, NEW_PEER)) >= 2, 60, "the approval confirmation")
        log(f"approval confirmation: {sent_to(fake, NEW_PEER)[-1]!r}")
        replies_before = len(sent_to(fake, NEW_PEER))
        fake.push(dm(NEW_PEER, "thanks!"))
        time.sleep(5)
        if any("Pairing code" in t for t in sent_to(fake, NEW_PEER)[replies_before:]):
            raise SystemExit("approved sender was asked to pair again")
    except BaseException as error:  # noqa: BLE001 - report every failure after cleanup
        failure = error
    finally:
        if gateway is not None and gateway.poll() is None:
            gateway.send_signal(signal.SIGTERM)
            try:
                gateway.wait(timeout=90)
            except subprocess.TimeoutExpired:
                os.killpg(gateway.pid, signal.SIGKILL)
                failure = failure or SystemExit("gateway did not stop within 90s of SIGTERM")
        if gateway is not None:
            time.sleep(1)
            leftovers = hkcli_sync_processes(hkcli)
            for pid, _ in leftovers:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            if leftovers:
                listing = "\n".join(f"{pid} {cmd}" for pid, cmd in leftovers)
                log(f"hkcli left running after the Gateway stopped:\n{listing}")
                failure = failure or SystemExit("hkcli sync was left running after the Gateway stopped")
            else:
                log("no hkcli sync left running after the Gateway stopped")
            log(f"gateway exit code: {gateway.returncode}")
            lines = gateway_log.read_text(errors="replace").splitlines() if gateway_log.exists() else []
            lines = [line for line in lines if "startup phase:" not in line]
            print("----- gateway log (tail) -----\n" + "\n".join(lines[-(600 if failure else 150):]), flush=True)
        fake.stop()
    if failure is not None:
        log(f"FAILED: {failure}")
        return 1
    log("passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
