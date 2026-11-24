import json
import os
import re
import signal
import sqlite3
import subprocess
import uuid
from pathlib import Path

import pytest

from fakehike import GROUP, NEW_PEER, PEER, T0, USER
from lockhelper import FileLock


def _session(store: Path) -> dict:
    return json.loads((store / "session.json").read_text(encoding="utf-8"))


def _start(cli, *args) -> subprocess.Popen:
    return subprocess.Popen([str(cli.bin), "--json", *args], env=dict(os.environ), stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8")


def test_qr_login_saves_session_and_syncs(fake, store, cli, logged_in):
    session = _session(store)
    assert (session["user_id"], session["access_token"], session["login_method"]) == (USER, fake.access_token, "qr")
    assert fake.qr_device_ids == [json.loads((store / "device.json").read_text())["deviceId"]]
    assert logged_in["sync"]["chats"] == 2  # the self-conversation is skipped like Hike Web does

    code, out, _ = cli("chats", "list")
    chats = {c["name"]: c for c in out["data"]}
    assert set(chats) == {"Asha", "Family"}
    assert chats["Asha"]["unread"] == 2 and chats["Asha"]["last_text"] == "This message was deleted"
    assert chats["Family"]["type"] == "group" and chats["Family"]["muted"]


def test_messages_list_and_search(cli, logged_in):
    code, out, _ = cli("messages", "list", "--chat", "Asha")
    msgs = out["data"]
    assert [m["text"] for m in msgs] == ["hello from asha", "hi asha, lunch tomorrow?", "menu photo", "see you at noon", None]
    assert msgs[1]["from_me"] and msgs[1]["state"] == "read"
    assert msgs[2]["kind"] == "image" and msgs[2]["has_media"]
    assert msgs[3]["reactions"] == {USER: "👍"}
    assert msgs[4]["deleted"]

    assert [m["text"] for m in cli("messages", "search", "lunch")[1]["data"]] == ["hi asha, lunch tomorrow?"]
    assert cli("messages", "search", "secret")[1]["data"] == []
    hit = cli("messages", "search", "dinn*")[1]["data"][0]
    assert (hit["chat_name"], hit["sender_name"]) == ("Family", "Ravi")


def test_send_text_matches_hike_web_packet(fake, store, cli, logged_in):
    code, out, err = cli("send", "text", "--to", "Asha", "--message", "on my way 🚗")
    assert code == 0, err
    topic, packet = fake.published[-1]
    assert topic == USER
    assert (packet["t"], packet["st"], packet["f"], packet["c"], packet["to"]) == ("m", "text", USER, PEER, PEER)
    assert packet["d"] == {"st": "text", "ts": packet["ts"], "i": packet["i"], "m": "on my way 🚗", "p": "on my way 🚗"}
    assert re.fullmatch(r"\d+:\d+", out["data"]["server_id"]) and out["data"]["client_id"] == packet["i"]
    connect = fake.mqtt_connects[-1]
    assert connect["client_id"].startswith(f"web_{USER}_") and connect["level"] == 4 and connect["keepalive"] == 25

    stored = cli("messages", "list", "--chat", PEER, "--limit", "1")[1]["data"][0]
    assert (stored["text"], stored["state"], stored["server_id"]) == ("on my way 🚗", "sent", out["data"]["server_id"])

    target = f"23:{T0 + 4000}"
    assert cli("send", "text", "--to", "Asha", "--message", "great", "--reply-to", target)[0] == 0
    assert fake.packets("m")[-1]["rd"] == {"s": target, "f": PEER, "st": "text", "pt": "see you at noon", "tn": None}

    code, out, _ = cli("send", "text", "--to", "+91 98000 00009", "--message", "hey neel")
    assert (out["data"]["to"], out["data"]["name"]) == (NEW_PEER, "Neel")
    assert fake.packets("m")[-1]["to"] == NEW_PEER


def test_expired_token_is_refreshed_for_relay_and_api(fake, store, cli, logged_in):
    fake.expire_access()
    code, out, err = cli("chats", "mark-read", "--chat", "Asha")
    assert code == 0, err
    assert fake.refresh_calls == 1
    assert (_session(store)["access_token"], _session(store)["refresh_token"]) == ("access-2", "refresh-2")
    assert [c["accepted"] for c in fake.mqtt_connects[-2:]] == [False, True]
    packet = fake.packets("fmr")[-1]  # unread according to the API -> "first mark read", like Hike Web
    assert packet == {"t": "fmr", "f": USER, "to": PEER, "d": {"s": f"24:{T0 + 5000}"}, "ts": packet["ts"]}

    fake.expire_access()
    code, _, err = cli("contacts", "refresh")
    assert code == 0, err
    assert fake.refresh_calls == 2


def test_rejected_refresh_asks_for_login_again(fake, store, cli, logged_in):
    fake.refresh_token = "server-forgot-it"
    fake.expire_access()
    code, out, _ = cli("contacts", "refresh")
    assert code == 3 and out["error"]["code"] == "auth_required"
    status = cli("auth", "status")[1]["data"]
    assert status["logged_in"] is False and "refresh rejected" in status["revoked"]


def test_sync_follow_stores_live_events(fake, store, cli, logged_in):
    code, sent, err = cli("send", "text", "--to", "Asha", "--message", "did you get this?")
    assert code == 0, err
    my_server_id = sent["data"]["server_id"]
    proc = subprocess.Popen(
        [str(cli.bin), "--json", "sync", "--idle-exit", "3"],
        env=dict(os.environ), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8",
    )
    try:
        assert fake.wait_for(lambda: fake.packets("fdr")), "sync never finished its catch-up"
        ts = T0 + 60_000
        fake.push({"t": "m", "st": "text", "f": PEER, "to": USER, "c": PEER, "i": str(uuid.uuid4()), "s": f"40:{ts}", "ts": ts,
                   "d": {"st": "text", "ts": ts, "m": "are you there?", "p": "are you there?"}})
        fake.push({"t": "r", "f": PEER, "to": USER, "i": str(uuid.uuid4()), "ts": ts + 1, "d": {"st": "ar", "r": "❤️", "sid": f"21:{T0 + 2000}"}})
        fake.push({"t": "ma", "c": PEER, "f": PEER, "to": USER, "ts": ts + 2, "d": {"st": "em", "sid": f"20:{T0 + 1000}", "m": "hello again"}})
        fake.push({"t": "mr", "f": PEER, "to": USER, "d": {USER: my_server_id}, "ts": ts + 3})
        presence_before = len(fake.packets("bg"))
        fake.kick()
        assert fake.wait_for(lambda: len(fake.packets("bg")) > presence_before), "sync did not reconnect after the drop"
        fake.push({"t": "m", "st": "text", "f": PEER, "to": USER, "c": PEER, "i": str(uuid.uuid4()), "s": f"41:{ts + 9}", "ts": ts + 9,
                   "d": {"st": "text", "ts": ts + 9, "m": "still there?", "p": "still there?"}})
        fake.push({"t": "m", "st": "text", "f": PEER, "to": GROUP, "i": str(uuid.uuid4()), "s": f"42:{ts + 10}", "ts": ts + 10,
                   "d": {"st": "text", "ts": ts + 10, "m": "@Vinay lunch?", "md": [{"uid": USER, "num": "+919800000001", "start": 0, "length": 6}]}})
        out, err = proc.communicate(timeout=40)
    finally:
        proc.kill()
    assert proc.returncode == 0, err
    events = [json.loads(line) for line in out.splitlines() if line.strip()]
    kinds = [e["event"] for e in events]
    for expected in ("connected", "sync", "message", "reaction", "edit", "receipt", "stopped"):
        assert expected in kinds, (expected, kinds)
    first = next(e for e in events if e["event"] == "message")
    assert (first["text"], first["chat_type"], first["mentions"], first["mentions_me"]) == ("are you there?", "one_to_one", [], False)
    mention = next(e for e in events if e["event"] == "message" and e["chat"] == GROUP)
    assert (mention["chat_type"], mention["mentions_me"], mention["mentions"][0]["uid"]) == ("group", True, USER)
    assert "disconnected" in kinds and kinds.count("connected") == 2

    assert fake.packets("fdr")[0] == {"t": "fdr", "f": USER, "to": PEER, "d": {"s": f"24:{T0 + 5000}"}, "ts": fake.packets("fdr")[0]["ts"]}
    assert len(fake.packets("bg")) >= 2  # quiet presence on connect and on exit

    conn = sqlite3.connect(store / "hike.db")
    conn.row_factory = sqlite3.Row
    text_of = lambda sid: conn.execute("SELECT text, state FROM messages WHERE server_id=?", (sid,)).fetchone()  # noqa: E731
    assert text_of(f"40:{ts}")["text"] == "are you there?"
    assert text_of(f"41:{ts + 9}")["text"] == "still there?"  # arrived after the reconnect
    assert text_of(f"20:{T0 + 1000}")["text"] == "hello again"
    assert text_of(my_server_id)["state"] == "read"
    assert conn.execute("SELECT emoji FROM reactions WHERE target_id=? AND sender_id=?", (f"21:{T0 + 2000}", PEER)).fetchone()[0] == "❤️"


def test_commands_publish_through_a_running_sync(fake, store, cli, logged_in):
    # Hike's relay may stop delivering to a connection once a second one of the account appears.
    proc = subprocess.Popen(
        [str(cli.bin), "--json", "sync", "--idle-exit", "8"],
        env=dict(os.environ), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8",
    )
    try:
        assert fake.wait_for(lambda: fake.packets("fdr")), "sync never finished its catch-up"
        assert (store / "sync-control.json").exists(), "sync did not record its control endpoint"
        connects = len(fake.mqtt_connects)
        code, out, err = cli("send", "text", "--to", "Asha", "--message", "one connection")
        assert code == 0, err
        assert fake.packets("m")[-1]["d"]["m"] == "one connection"
        assert re.fullmatch(r"\d+:\d+", out["data"]["server_id"]) and out["data"]["via"] == "sync"
        assert cli("chats", "mark-read", "--chat", "Asha")[0] == 0
        relay = next(c for c in cli("doctor", "--connect")[1]["data"]["checks"] if c["check"] == "relay")
        assert relay["ok"] and "running hkcli sync" in relay["detail"], relay
        assert len(fake.mqtt_connects) == connects, "a command opened its own relay connection next to the sync's"
        _, err = proc.communicate(timeout=40)
    finally:
        proc.kill()
    assert proc.returncode == 0, err
    assert not (store / "sync-control.json").exists()

    code, out, err = cli("send", "text", "--to", "Asha", "--message", "no sync running")
    assert code == 0 and out["data"]["via"] == "own" and len(fake.mqtt_connects) == connects + 1, err


def test_sync_waits_while_a_command_sends_on_its_own_connection(fake, store, cli, logged_in):
    fake.reset_relay_peak()
    fake.puback_delay = 3.0
    send = _start(cli, "send", "text", "--to", "Asha", "--message", "before sync")
    procs = [send]
    try:
        assert fake.wait_for(lambda: any(p.get("d", {}).get("m") == "before sync" for p in fake.packets("m"))), "the send never reached the relay"
        sync = _start(cli, "sync", "--idle-exit", "3")
        procs.append(sync)
        out, err = send.communicate(timeout=30)
        assert send.returncode == 0 and json.loads(out)["data"]["via"] == "own", err
        out, err = sync.communicate(timeout=40)
        assert sync.returncode == 0, err
    finally:
        for proc in procs:
            proc.kill()
    statuses = [e.get("message", "") for e in map(json.loads, out.splitlines()) if e["event"] == "status"]
    assert any("waiting for another hkcli process" in s for s in statuses), statuses
    assert fake.peak_open_relays == 1, "two relay connections were open at once"


def test_commands_send_on_their_own_while_sync_waits_to_reconnect(fake, store, cli, logged_in):
    # A failed catch-up makes sync close its connection and wait (1 s, 2 s, then 4 s) before trying
    # again. Meanwhile commands may connect on their own, but never next to the sync's connection.
    fake.reset_relay_peak()
    fake.fail_conversations = 3
    sync = _start(cli, "sync", "--idle-exit", "4")
    try:
        assert fake.wait_for(lambda: fake.fail_conversations == 0 and fake.open_relays == 0, timeout=20), "sync did not retry its catch-up"
        code, out, err = cli("send", "text", "--to", "Asha", "--message", "while sync waits")
        assert code == 0 and out["data"]["via"] == "own", err
        assert fake.wait_for(lambda: fake.packets("fdr"), timeout=20), "sync never finished its catch-up"
        code, out, err = cli("send", "text", "--to", "Asha", "--message", "after the catch-up")
        assert code == 0 and out["data"]["via"] == "sync", err
        out, err = sync.communicate(timeout=40)
    finally:
        sync.kill()
    assert sync.returncode == 0, err
    assert [e["event"] for e in map(json.loads, out.splitlines())].count("disconnected") == 3
    assert fake.peak_open_relays == 1, "two relay connections were open at once"


@pytest.mark.skipif(os.name == "nt", reason="stops sync with SIGINT")
def test_stopping_sync_finishes_a_send_in_flight(fake, store, cli, logged_in):
    def start(*args):
        return subprocess.Popen([str(cli.bin), "--json", *args], env=dict(os.environ), stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8")

    sync = start("sync")
    procs = [sync]
    try:
        assert fake.wait_for(lambda: fake.packets("fdr")), "sync never finished its catch-up"
        fake.reset_relay_peak()
        fake.puback_delay = 2.0
        first = start("send", "text", "--to", "Asha", "--message", "in flight")
        procs.append(first)
        assert fake.wait_for(lambda: any(p.get("d", {}).get("m") == "in flight" for p in fake.packets("m"))), "the send never reached the relay"
        sync.send_signal(signal.SIGINT)  # before Hike acknowledged it
        second = start("send", "text", "--to", "Asha", "--message", "while sync stops")
        procs.append(second)
        out, err = first.communicate(timeout=30)
        assert first.returncode == 0 and json.loads(out)["data"]["server_id"], err
        out, err = second.communicate(timeout=60)
        assert second.returncode == 0 and json.loads(out)["data"]["server_id"], err
        sync.wait(timeout=30)
    finally:
        for proc in procs:
            proc.kill()
    assert fake.peak_open_relays == 1, "two relay connections were open at once"
    assert not (store / "sync-control.json").exists()


def test_send_file_and_download_media(fake, store, cli, logged_in, tmp_path):
    doc = tmp_path / "notes.txt"
    doc.write_bytes(b"0123456789abcdefghij")
    code, out, err = cli("send", "file", "--to", "Asha", "--file", str(doc), "--caption", "the notes")
    assert code == 0, err
    file_id = out["data"]["file_id"]
    assert fake.uploads["up-1"]["request"] == {"fileName": "notes.txt", "mimeType": "text/plain", "fileSizeBytes": 20}
    assert fake.files[file_id] == doc.read_bytes()
    packet = fake.packets("m")[-1]
    assert (packet["st"], packet["node"], packet["seq"]) == ("file", "default", packet["ts"])
    assert packet["d"] == {"st": "file", "ts": packet["ts"], "i": packet["i"], "fid": file_id, "p": "notes.txt", "s": 20, "mime": "text/plain", "fn": "notes.txt", "m": "the notes"}

    code, out, err = cli("media", "download", "--chat", "Asha", "--id", f"22:{T0 + 3000}", "--out", str(tmp_path))
    assert code == 0, err
    saved = Path(out["data"]["path"])
    assert saved.read_bytes() == fake.files["file-1"] and saved.parent == tmp_path
    storage = [r for r in fake.http_log if r["path"].startswith(("/put/", "/blob/"))]
    assert storage and all("Authorization" not in r["headers"] for r in storage)  # login token never leaves Hike's API
    assert sorted(r["headers"]["Range"] for r in storage if r["path"].startswith("/blob/")) == ["bytes=0-15", "bytes=16-29"]


def test_history_backfill_reaches_the_start(cli, logged_in):
    code, out, _ = cli("history", "backfill", "--chat", "Asha", "--batch", "5", "--pages", "10")
    assert out["data"]["complete"] and out["data"]["new_messages"] == 12
    msgs = cli("messages", "list", "--chat", "Asha", "--limit", "100")[1]["data"]
    assert len(msgs) == 17 and msgs[0]["text"] == "old message 1"


def test_read_only_blocks_changes_but_allows_reading(cli, logged_in, monkeypatch):
    code, out, _ = cli("--read-only", "send", "text", "--to", "Asha", "--message", "x")
    assert code == 1 and out["error"]["code"] == "read_only"
    monkeypatch.setenv("HKCLI_READONLY", "1")
    assert cli("sync", "--once")[1]["error"]["code"] == "read_only"
    code, out, _ = cli("chats", "list")
    assert code == 0 and len(out["data"]) == 2


def test_otp_login_retries_a_wrong_code(fake, store, cli):
    code, out, err = cli("auth", "--phone", "+91 98000 00001", "--no-sync", input="000000\n123456\n")
    assert code == 0, err
    device = json.loads((store / "device.json").read_text())["deviceId"]
    assert fake.otp_requests == [{"mobileNumber": "9800000001", "countryCode": "+91", "deviceType": "WEB", "deviceFingerprint": device}]
    assert "Invalid OTP" in err
    assert (_session(store)["user_id"], _session(store)["login_method"]) == (USER, "otp")


def test_second_sync_is_refused_while_store_is_locked(store, cli, logged_in):
    lock = FileLock(store / "LOCK")
    assert lock.acquire()
    try:
        code, out, _ = cli("sync", "--once")
        assert code == 1 and out["error"]["code"] == "store_locked"
    finally:
        lock.release()


def test_doctor_groups_and_logout(fake, store, cli, logged_in):
    code, out, _ = cli("doctor", "--connect")
    assert code == 0 and out["data"]["ok"], out
    assert cli("auth", "status", "--check")[1]["data"]["server_check"] == "ok"
    members = cli("groups", "info", "--group", "Family")[1]["data"]["members"]
    assert [m["name"] for m in members] == ["Me", "Ravi"]
    assert cli("groups", "list")[1]["data"][0]["id"] == GROUP

    code, out, _ = cli("auth", "logout")
    assert out["data"]["server_logout"] == "ok"
    assert fake.logout_requests == [{"refreshToken": "refresh-1", "logoutAllDevices": False}]
    assert not (store / "session.json").exists()
