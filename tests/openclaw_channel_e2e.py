"""End-to-end check of the OpenClaw Hike channel plugin against the fake Hike servers (no LLM).

Installs the packed plugin into an isolated OpenClaw state dir, links the agent's Hike account
through `openclaw channels login` (the fake confirms the QR login by itself) and runs a real
`openclaw gateway`. Then, over the fake relay: an unknown sender's DM gets the pairing reply;
approving it through the Gateway with notify (the Control UI path) sends the confirmation and the
sender's next DM reaches the agent; in an allowed group only the @mention reaches the agent.
The Gateway is then killed hard: the next run must stop the hkcli sync it left behind. A DM that
hkcli stores while the Gateway is down must reach the agent after a restart, and no earlier DM may
be answered twice. Finally `channels logout` signs the device out, and no hkcli sync may be left
running. Without a model the agent turn itself fails, so dispatch is read from the Gateway's
diagnostics.

Usage: python tests/openclaw_channel_e2e.py --hkcli PATH --plugin PATH.tgz
Needs Linux, `openclaw` on PATH, hkcli 0.3.0+ built from this repo, and requirements-dev.txt.
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
from fakehike import GROUP, NEW_PEER, PEER, PEER2, USER, FakeHike  # noqa: E402


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


def group_message(sender: str, text: str, mention_me: bool) -> dict:
    ts = int(time.time() * 1000)
    data = {"st": "text", "ts": ts, "m": text, "p": text}
    if mention_me:
        data["md"] = [{"uid": USER, "num": "+919800000001", "start": 0, "length": 3}]
    return {"t": "m", "st": "text", "f": sender, "to": GROUP, "i": str(uuid.uuid4()), "s": f"{ts % 100000}:{ts}", "ts": ts, "d": data}


def dispatched(log_file: Path, chat: str) -> bool:
    """The Gateway logs one `message processed` diagnostic per message handed to the agent."""
    return bool(processed(log_file, chat))


def processed(log_file: Path, chat: str) -> list[str]:
    """Message ids of the `message processed` diagnostics for `chat`."""
    text = log_file.read_text(errors="replace") if log_file.exists() else ""
    return re.findall(rf"message processed: channel=hike chatId=\S*{re.escape(chat)}\b messageId=(\S+)", text)


def relay_subscribed(fake: FakeHike) -> bool:
    with fake.lock:
        return any(f"u:{USER}" in s["topics"] for s in fake.sessions)


def relay_sessions(fake: FakeHike) -> list[dict]:
    with fake.lock:
        return list(fake.sessions)


def new_relay_session(fake: FakeHike, known: list[dict]) -> bool:
    """A relay session subscribed since `known` was taken (it holds the old ones, so ids stay unique)."""
    with fake.lock:
        return any(f"u:{USER}" in s["topics"] and all(s is not old for old in known) for s in fake.sessions)


def is_running(pid: int) -> bool:
    try:
        with open(f"/proc/{pid}/stat") as stat:
            return stat.read().rsplit(")", 1)[1].split()[0] != "Z"
    except FileNotFoundError:
        return False


def hkcli_sync_processes(hkcli: str, store: Path) -> list[tuple[int, str]]:
    """`hkcli sync` processes on this test's store; other runs may use the same binary."""
    proc = subprocess.run(["pgrep", "-af", hkcli], capture_output=True, text=True)
    found = []
    for line in proc.stdout.splitlines():
        pid, _, cmd = line.partition(" ")
        args = cmd.split()
        on_store = any(a == "--store" and b == str(store) for a, b in zip(args, args[1:]))
        if args and args[0] == hkcli and on_store and "sync" in args:
            found.append((int(pid), cmd))
    return found


def start_gateway(openclaw: str, port: int, env: dict, log_file: Path) -> subprocess.Popen:
    with log_file.open("w") as out:
        return subprocess.Popen(
            [openclaw, "gateway", "run", "--port", str(port), "--bind", "loopback", "--verbose"],
            env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT, start_new_session=True,
        )


def stop_gateway(gateway: subprocess.Popen) -> None:
    gateway.send_signal(signal.SIGTERM)
    try:
        gateway.wait(timeout=90)
    except subprocess.TimeoutExpired:
        os.killpg(gateway.pid, signal.SIGKILL)
        raise SystemExit("gateway did not stop within 90s of SIGTERM")


def crash_gateway(gateway: subprocess.Popen, hkcli: str, store: Path) -> int:
    """SIGKILL every Gateway process except its hkcli sync, like an OOM kill. Returns the sync's pid."""
    syncs = hkcli_sync_processes(hkcli, store)
    if len(syncs) != 1:
        raise SystemExit(f"expected one hkcli sync, found {syncs}")
    orphan = syncs[0][0]
    group = [int(pid) for pid in subprocess.run(["pgrep", "-g", str(gateway.pid)], capture_output=True, text=True).stdout.split()]
    if orphan not in group:
        raise SystemExit("hkcli sync does not belong to the Gateway's process group")
    for pid in group:
        if pid != orphan:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
    gateway.wait(timeout=30)
    return orphan


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
    store = state / "hike" / "default"
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
    standalone = None
    gateway_logs: list[Path] = []
    failure = None
    try:
        run([hkcli, "--version"], env)
        run([openclaw, "--version"], env)
        run([openclaw, "plugins", "install", f"npm-pack:{plugin}", "--force", "--accept-capabilities"], env)
        run([openclaw, "config", "set", "gateway.mode", "local"], env)
        run([openclaw, "config", "set", "gateway.port", str(port), "--strict-json"], env)
        run([openclaw, "config", "set", "channels.hike.enabled", "true", "--strict-json"], env)
        run([openclaw, "config", "set", "channels.hike.cliPath", hkcli], env)
        run([openclaw, "config", "set", "channels.hike.groups", json.dumps({GROUP: {"allowFrom": ["*"]}}), "--strict-json"], env)

        run([openclaw, "channels", "login", "--channel", "hike"], env, timeout=180)
        session = store / "session.json"
        if not session.is_file():
            raise SystemExit(f"login did not create {session}")
        log(f"linked; store {store}")

        connects_before = len(fake.mqtt_connects)
        gateway_log = work / "gateway.log"
        gateway_logs.append(gateway_log)
        gateway = start_gateway(openclaw, port, env, gateway_log)
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
        wait_for(lambda: dispatched(gateway_log, NEW_PEER), 60, "the approved sender's DM to reach the agent")
        if any("Pairing code" in t for t in sent_to(fake, NEW_PEER)[replies_before:]):
            raise SystemExit("approved sender was asked to pair again")
        log("approved sender's DM was dispatched to the agent")

        fake.push(group_message(PEER2, "dinner at 8?", mention_me=False))
        time.sleep(5)
        if dispatched(gateway_log, GROUP):
            raise SystemExit("a group message without a mention reached the agent")
        fake.push(group_message(PEER2, "@Me are you joining?", mention_me=True))
        wait_for(lambda: dispatched(gateway_log, GROUP), 60, "the @mention in the group to reach the agent")
        log("group: unmentioned message dropped, @mention dispatched")

        # A Gateway that dies hard leaves its hkcli sync holding the store; the next run stops it.
        time.sleep(3)
        orphan = crash_gateway(gateway, hkcli, store)
        time.sleep(2)
        if not is_running(orphan):
            raise SystemExit("hkcli sync did not outlive the killed Gateway")
        log(f"gateway killed; its hkcli sync (pid {orphan}) is still running")
        known = relay_sessions(fake)
        gateway_log = work / "gateway-2.log"
        gateway_logs.append(gateway_log)
        gateway = start_gateway(openclaw, port, env, gateway_log)
        reclaimed = rf"stopped leftover hkcli sync \(pid {orphan}\)"
        wait_for(lambda: gateway.poll() is not None or re.search(reclaimed, gateway_log.read_text(errors="replace")),
                 120, "the restarted Gateway to stop the leftover hkcli sync")
        if gateway.poll() is not None:
            raise SystemExit(f"gateway exited early with code {gateway.returncode}")
        wait_for(lambda: not is_running(orphan), 30, "the leftover hkcli sync to exit")
        wait_for(lambda: new_relay_session(fake, known), 120, "the restarted Gateway's hkcli sync to connect")
        log("the restarted Gateway stopped the leftover hkcli sync and reconnected")
        time.sleep(5)

        # A DM that hkcli stores while the channel is down is answered after a restart, even though
        # hkcli's next catch-up then has nothing new to fetch.
        stop_gateway(gateway)
        wait_for(lambda: not hkcli_sync_processes(hkcli, store), 30, "hkcli sync to stop with the Gateway")
        if processed(gateway_log, NEW_PEER):
            raise SystemExit(f"DMs answered before the crash reached the agent again: {processed(gateway_log, NEW_PEER)}")
        known = relay_sessions(fake)
        standalone = subprocess.Popen(
            [hkcli, "--store", str(store), "--json", "sync", "--idle-exit", "8"],
            env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        wait_for(lambda: standalone.poll() is not None or new_relay_session(fake, known), 60, "a standalone hkcli sync to connect")
        time.sleep(2)
        while_down = dm(NEW_PEER, "are you there?")
        fake.push(while_down)
        out, err = standalone.communicate(timeout=60)
        events = [json.loads(line) for line in out.splitlines() if line.startswith("{")]
        if not any(event.get("event") == "message" and event.get("id") == while_down["s"] for event in events):
            raise SystemExit(f"the standalone hkcli sync did not store the DM:\n{out}\n{err}")
        log("hkcli stored a DM while the Gateway was down")

        pairing_replies = sum("Pairing code" in t for t in sent_to(fake, NEW_PEER))
        gateway_log = work / "gateway-3.log"
        gateway_logs.append(gateway_log)
        gateway = start_gateway(openclaw, port, env, gateway_log)
        wait_for(lambda: gateway.poll() is not None or processed(gateway_log, NEW_PEER), 120,
                 "the DM stored while the Gateway was down to reach the agent")
        time.sleep(5)
        after_restart = processed(gateway_log, NEW_PEER)
        if after_restart != [while_down["s"]]:
            raise SystemExit(f"after the restart expected only {while_down['s']} to reach the agent, got {after_restart}")
        if sum("Pairing code" in t for t in sent_to(fake, NEW_PEER)) != pairing_replies:
            raise SystemExit("an old DM got a pairing reply again after the restart")
        log("after the restart only the DM stored while the Gateway was down reached the agent")

        status = run([openclaw, "channels", "status", "--channel", "hike", "--probe", "--json"], env, timeout=120, check=False)
        if '"linked": true' not in status.stdout.replace('"linked":true', '"linked": true'):
            log("warning: channels status did not report the account as linked")

        run([openclaw, "channels", "logout", "--channel", "hike"], env, timeout=120)
        if session.exists():
            raise SystemExit("logout left session.json behind")
        if not fake.logout_requests:
            raise SystemExit("logout did not sign the device out of Hike")
        wait_for(lambda: not hkcli_sync_processes(hkcli, store), 30, "hkcli sync to stop after logout")
        log("logout stopped the account and signed out of Hike")
    except BaseException as error:  # noqa: BLE001 - report every failure after cleanup
        failure = error
    finally:
        if standalone is not None and standalone.poll() is None:
            standalone.kill()
        if gateway is not None and gateway.poll() is None:
            try:
                stop_gateway(gateway)
            except SystemExit as error:
                failure = failure or error
        if gateway is not None:
            time.sleep(1)
            leftovers = hkcli_sync_processes(hkcli, store)
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
        for index, path in enumerate(gateway_logs):
            last = index == len(gateway_logs) - 1
            lines = path.read_text(errors="replace").splitlines() if path.exists() else []
            lines = [line for line in lines if "startup phase:" not in line]
            keep = (600 if last else 300) if failure else (150 if last else 60)
            print(f"----- {path.name} (tail) -----\n" + "\n".join(lines[-keep:]), flush=True)
        fake.stop()
    if failure is not None:
        log(f"FAILED: {failure}")
        return 1
    log("passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
