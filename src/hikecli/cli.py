"""hikecli command line."""

from __future__ import annotations

import argparse
import json
import os
import re
import sqlite3
import sys
import threading
import webbrowser
from datetime import datetime, timezone
from pathlib import Path

from . import __version__, db
from .api import AuthRequired, HikeAPI, HikeError
from .auth import LoginError, enable_ansi, otp_login, qr_login, render_qr, save_qr_png
from .config import api_base, mqtt_ws_url, session_ws_base
from .media import download, guess_mime, upload
from .mqtt import MQTTError
from .phone import looks_like_phone, normalize_e164, split_phone
from .protocol import (
    as_str,
    build_file,
    build_first_mark_read,
    build_mark_read,
    build_text,
    now_ms,
    parse_message,
    summary,
)
from .store import FileLock, Session, Store, delete_session, device_id, load_session, save_session
from .sync import Emitter, Syncer, fmt_time, open_mqtt, publish_packet

EXIT_OK, EXIT_ERROR, EXIT_USAGE, EXIT_AUTH = 0, 1, 2, 3


class CLIError(Exception):
    def __init__(self, message: str, code: str = "error", exit_code: int = EXIT_ERROR):
        super().__init__(message)
        self.code = code
        self.exit_code = exit_code


class Ctx:
    def __init__(self, args: argparse.Namespace):
        self.args = args
        self.json = bool(getattr(args, "json", False))
        env_ro = os.environ.get("HIKECLI_READONLY", "").strip().lower() not in ("", "0", "false", "no")
        self.read_only = bool(getattr(args, "read_only", False)) or env_ro
        self.store = Store(getattr(args, "store", None))
        self._conn: sqlite3.Connection | None = None
        self._api: HikeAPI | None = None

    def conn(self) -> sqlite3.Connection:
        if self._conn is None:
            self.store.ensure()
            self._conn = db.connect(self.store.db_path)
        return self._conn

    def session(self) -> Session:
        session = load_session(self.store)
        if session is None:
            raise AuthRequired("not logged in; run: hikecli auth")
        if session.revoked:
            raise AuthRequired(f"the saved login no longer works ({session.revoked}); run: hikecli auth")
        return session

    def api(self) -> HikeAPI:
        if self._api is None:
            self._api = HikeAPI(self.store, self.session())
        return self._api

    def me(self) -> str:
        session = load_session(self.store)
        if session:
            return session.user_id
        return db.get_meta(self.conn(), "user_id") or ""

    def require_writable(self, what: str) -> None:
        if self.read_only:
            raise CLIError(f"'{what}' is blocked by --read-only (HIKECLI_READONLY)", code="read_only")

    def output(self, data, human) -> None:
        if self.json:
            print(json.dumps({"success": True, "data": data, "error": None}, ensure_ascii=False, default=str))
        else:
            human(data)

    def close(self) -> None:
        if self._conn is not None:
            self._conn.close()
            self._conn = None


# ---- helpers -------------------------------------------------------------------


def status(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def iso(ms: int | None) -> str | None:
    return datetime.fromtimestamp(ms / 1000, tz=timezone.utc).isoformat() if ms else None


def clip(text, width: int) -> str:
    text = "" if text is None else str(text).replace("\r", " ").replace("\n", " ")
    return text if len(text) <= width else text[: max(0, width - 1)] + "…"


def print_table(headers: list[str], rows: list[list], widths: dict[int, int] | None = None) -> None:
    widths = widths or {}
    cells = [[clip(value, widths.get(i, 60)) for i, value in enumerate(row)] for row in rows]
    sizes = [max([len(h)] + [len(r[i]) for r in cells]) for i, h in enumerate(headers)]
    print("  ".join(h.ljust(sizes[i]) for i, h in enumerate(headers)).rstrip())
    for row in cells:
        print("  ".join(v.ljust(sizes[i]) for i, v in enumerate(row)).rstrip())


def store_lock(ctx: Ctx, what: str) -> FileLock:
    ctx.store.ensure()
    lock = FileLock(ctx.store.lock_path)
    if not lock.acquire(timeout=0):
        pid = lock.holder_pid()
        raise CLIError(f"another hikecli process{f' (pid {pid})' if pid else ''} is already running {what} on this store", code="store_locked")
    return lock


_UNSAFE_NAME = re.compile(r'[<>:"/\\|?*\x00-\x1f]')
_WINDOWS_DEVICES = {"CON", "PRN", "AUX", "NUL", *(f"COM{i}" for i in range(1, 10)), *(f"LPT{i}" for i in range(1, 10))}


def safe_filename(name: str) -> str:
    """Make a remote-supplied name safe to use as a single file name on any OS."""
    name = _UNSAFE_NAME.sub("_", Path(name.replace("\\", "/")).name).strip(" .")
    if name.split(".")[0].upper() in _WINDOWS_DEVICES:
        name = f"_{name}"
    return name[:180] or "file"


def parse_time(value: str | None) -> int | None:
    if not value:
        return None
    if value.isdigit():
        return int(value)
    for fmt in ("%Y-%m-%d %H:%M", "%Y-%m-%dT%H:%M", "%Y-%m-%d"):
        try:
            return int(datetime.strptime(value, fmt).timestamp() * 1000)
        except ValueError:
            pass
    raise CLIError(f"cannot read time {value!r}; use YYYY-MM-DD, 'YYYY-MM-DD HH:MM' or epoch milliseconds", code="invalid_input")


def resolve_chat(ctx: Ctx, spec: str, *, remote: bool = False) -> tuple[str, str]:
    """Accept a chat/user id, a phone number, or a (unique) name. Returns (id, display name)."""
    conn = ctx.conn()
    spec = (spec or "").strip()
    if not spec:
        raise CLIError("empty chat / recipient", code="invalid_input")
    row = db.get_chat(conn, spec)
    if row is not None:
        return spec, row["name"] or spec
    contact = conn.execute("SELECT * FROM contacts WHERE user_id=?", (spec,)).fetchone()
    if contact is not None:
        return spec, contact["name"] or contact["phone"] or spec
    if looks_like_phone(spec):
        e164 = normalize_e164(spec)
        digits = re.sub(r"\D", "", spec)
        matches = {}
        for r in conn.execute("SELECT user_id AS id, name, phone FROM contacts WHERE phone IS NOT NULL UNION SELECT id, name, phone FROM chats WHERE phone IS NOT NULL"):
            if re.sub(r"\D", "", r["phone"]).endswith(digits):
                matches[r["id"]] = r["name"] or r["phone"]
        if len(matches) == 1:
            return next(iter(matches.items()))
        if len(matches) > 1:
            listed = ", ".join(f"{n} ({i})" for i, n in matches.items())
            raise CLIError(f"{spec} matches several people: {listed}; use the id", code="ambiguous")
        if remote and e164:
            for profile in ctx.api().profiles_by_phone([e164]):
                uid = as_str(profile.get("userId"))
                if uid:
                    db.upsert_contact(conn, uid, name=as_str(profile.get("name")), phone=as_str(profile.get("phone")) or e164, overwrite_name=False)
                    conn.commit()
                    return uid, as_str(profile.get("name")) or e164
        hint = "" if e164 else " (include the country code, e.g. +91...)"
        raise CLIError(f"no Hike user found for {spec}{hint}", code="not_found")
    needle = spec.lower()
    candidates: dict[str, str] = {}
    for r in conn.execute("SELECT id, name FROM chats WHERE lower(name)=? UNION SELECT user_id, name FROM contacts WHERE lower(name)=?", (needle, needle)):
        candidates[r["id"]] = r["name"]
    if not candidates:
        like = f"%{needle}%"
        for r in conn.execute("SELECT id, name FROM chats WHERE lower(name) LIKE ? UNION SELECT user_id, name FROM contacts WHERE lower(name) LIKE ?", (like, like)):
            candidates[r["id"]] = r["name"]
    if len(candidates) == 1:
        return next(iter(candidates.items()))
    if candidates:
        listed = ", ".join(f"{n} ({i})" for i, n in list(candidates.items())[:10])
        raise CLIError(f"'{spec}' matches {len(candidates)} chats/contacts: {listed}; use the id", code="ambiguous")
    raise CLIError(f"no chat or contact matches '{spec}'; run 'hikecli sync' or use an id / phone number", code="not_found")


def chat_dict(row) -> dict:
    return {
        "id": row["id"],
        "type": row["type"],
        "name": row["name"],
        "phone": row["phone"],
        "unread": row["unread"],
        "archived": bool(row["archived"]),
        "muted": bool(row["muted"]),
        "pinned": bool(row["pinned"]),
        "blocked": bool(row["blocked"]),
        "last_ts": row["last_ts"],
        "last_time": iso(row["last_ts"]),
        "last_text": row["last_text"],
    }


def message_dict(conn: sqlite3.Connection, row, me: str, chat_name: str | None = None, raw: bool = False) -> dict:
    target = row["server_id"] or row["client_id"]
    data = {
        "id": target,
        "server_id": row["server_id"],
        "client_id": row["client_id"],
        "chat": row["chat_id"],
        "chat_name": chat_name,
        "ts": row["ts"],
        "time": iso(row["ts"]),
        "from_me": bool(row["from_me"]),
        "sender": row["sender_id"],
        "sender_name": "me" if row["from_me"] else (row["sender_name"] or db.display_name(conn, row["sender_id"], me)),
        "kind": row["kind"],
        "text": row["text"],
        "file_name": row["file_name"],
        "file_size": row["file_size"],
        "mime": row["mime"],
        "has_media": bool(row["file_id"]),
        "state": row["state"],
        "reply_to": row["reply_to_id"],
        "edited": bool(row["edited"]),
        "deleted": bool(row["deleted_for_all"]),
        "starred": bool(row["starred"]),
        "reactions": db.reactions_for(conn, row["chat_id"], target) if target else {},
    }
    if row["kind"] == "location":
        data.update(latitude=row["latitude"], longitude=row["longitude"], address=row["address"])
    if row["kind"] == "contact":
        data.update(contact_name=row["contact_name"], contact_phone=row["contact_phone"], contact_user_id=row["contact_user_id"])
    if raw:
        data["local_path"] = row["local_path"]
        data["raw"] = json.loads(row["raw"]) if row["raw"] else None
    return data


def message_line(m: dict, *, with_chat: bool = False) -> str:
    text = summary(m["kind"], m["text"], m["file_name"], m["deleted"])
    if m["edited"] and not m["deleted"]:
        text += " (edited)"
    if m["reactions"]:
        text += "  [" + " ".join(m["reactions"].values()) + "]"
    where = f"{m['chat_name']} / " if with_chat and m.get("chat_name") and m["chat_name"] != m["sender_name"] else ""
    return f"{fmt_time(m['ts'])}  {where}{m['sender_name']}: {text}   ({m['id']})"


def send_packet(ctx: Ctx, packet: dict) -> str | None:
    api = ctx.api()
    client = open_mqtt(api)
    try:
        return publish_packet(client, api.session.user_id, packet)
    finally:
        client.disconnect()


def store_sent(ctx: Ctx, packet: dict, server_id: str | None) -> None:
    conn = ctx.conn()
    msg = parse_message(packet, ctx.me())
    if msg is None:
        return
    msg.server_id = server_id if server_id and re.match(r"^\d+:\d+$", server_id) else None
    msg.state = "sent"
    db.ensure_chat(conn, msg.chat_id)
    db.upsert_message(conn, msg)
    db.touch_chat_last(conn, msg)
    conn.commit()


def reply_info(ctx: Ctx, chat_id: str, message_id: str | None) -> dict | None:
    if not message_id:
        return None
    row = db.find_message(ctx.conn(), chat_id, message_id)
    if row is None:
        raise CLIError(f"message {message_id} not found in this chat locally; run 'hikecli sync' first", code="not_found")
    return {"id": row["server_id"] or row["client_id"], "sender_id": row["sender_id"], "subtype": row["subtype"], "text": row["text"] or ""}


# ---- auth ----------------------------------------------------------------------


def _move_other_account_db(ctx: Ctx, user_id: str) -> str | None:
    if not ctx.store.db_path.exists():
        return None
    probe = db.connect(ctx.store.db_path)
    try:
        owner = db.get_meta(probe, "user_id")
    finally:
        probe.close()
    if not owner or owner == user_id:
        return None
    ctx.close()
    backup = ctx.store.root / f"hike.db.{safe_filename(owner)}.{now_ms()}.bak"
    os.replace(ctx.store.db_path, backup)
    for suffix in ("-wal", "-shm"):
        extra = Path(str(ctx.store.db_path) + suffix)
        if extra.exists():
            os.replace(extra, Path(str(backup) + suffix))
    return str(backup)


def cmd_auth(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("auth")
    existing = load_session(ctx.store)
    if existing and not existing.revoked and not a.force:
        raise CLIError(
            f"already logged in as {existing.display_name} ({existing.user_id}); run 'hikecli auth logout' first or pass --force",
            code="already_logged_in",
        )
    lock = store_lock(ctx, "a login or sync")
    try:
        dev = device_id(ctx.store)
        if a.phone:
            try:
                country_code, national = split_phone(a.phone)
            except ValueError as exc:
                raise CLIError(str(exc), code="invalid_input") from exc
            status(f"Asking Hike to send an OTP to {country_code} {national} ...")

            def read_otp(expires: int) -> str:
                hint = f" (valid {expires}s)" if expires else ""
                print(f"Enter the OTP you received{hint}: ", end="", file=sys.stderr, flush=True)
                return sys.stdin.readline()

            payload = otp_login(HikeAPI(ctx.store), dev, country_code, national, read_otp=read_otp)
            method = "otp"
        else:
            ansi = enable_ansi()

            def show_qr(token: str, seconds: int) -> None:
                try:
                    save_qr_png(token, ctx.store.qr_png_path)
                except OSError:
                    pass
                print("", file=sys.stderr)
                if a.qr_format == "text":
                    status(f"QR text (encode it as a QR code to scan it): {token}")
                elif a.qr_format == "png":
                    status(f"QR image: {ctx.store.qr_png_path}")
                    try:
                        if os.name == "nt":
                            os.startfile(ctx.store.qr_png_path)  # type: ignore[attr-defined]
                        else:
                            webbrowser.open(ctx.store.qr_png_path.as_uri())
                    except OSError:
                        pass
                else:
                    print(render_qr(token, ansi=ansi), file=sys.stderr, flush=True)
                    status(f"If it does not scan, open {ctx.store.qr_png_path}")
                status(f"On your phone: open Hike > Settings > Web QR Login > scan this code (new code in about {max(seconds - 20, 1)}s).")

            payload = qr_login(dev, on_qr=show_qr, on_status=status, timeout=a.timeout)
            method = "qr"
        user_id = as_str(payload.get("userId"))
        if not user_id:
            raise LoginError("Hike did not return a user id")
        profile = payload.get("profile") if isinstance(payload.get("profile"), dict) else {}
        session = Session(
            user_id=user_id,
            access_token=payload["accessToken"],
            refresh_token=payload["refreshToken"],
            profile=profile,
            login_method=method,
            created_at=now_ms(),
        )
        moved = _move_other_account_db(ctx, user_id)
        save_session(ctx.store, session)
        try:
            ctx.store.qr_png_path.unlink(missing_ok=True)
        except OSError:
            pass
        conn = ctx.conn()
        db.set_meta(conn, "user_id", user_id)
        conn.commit()
        ctx._api = HikeAPI(ctx.store, session)
        result = {
            "user_id": user_id,
            "name": profile.get("name"),
            "phone": profile.get("phone"),
            "method": method,
            "store": str(ctx.store.root),
            "previous_account_db": moved,
            "sync": None,
        }
        if moved:
            status(f"This store belonged to another account; its database was moved to {moved}")
        status(f"Logged in as {session.display_name} ({user_id}).")
        if not a.no_sync:
            status("Syncing contacts, chats and recent messages ...")
            syncer = Syncer(conn, ctx._api, user_id, Emitter(json_mode=False, quiet=True), max_chats=a.max_chats, batch_size=a.batch)
            result["sync"] = syncer.catch_up()

        def human(d):
            if d["sync"]:
                s = d["sync"]
                print(f"Synced {s['chats']} chats, {s['messages']} messages, {s['contacts']} contacts.")
            print("Next: 'hikecli chats list', 'hikecli sync' to stay up to date, 'hikecli send text --to <name> --message hi'.")

        ctx.output(result, human)
        return EXIT_OK
    finally:
        lock.release()


def cmd_auth_status(ctx: Ctx) -> int:
    session = load_session(ctx.store)
    data = {
        "logged_in": bool(session and not session.revoked),
        "user_id": session.user_id if session else None,
        "name": session.profile.get("name") if session else None,
        "phone": session.profile.get("phone") if session else None,
        "method": session.login_method if session else None,
        "since": iso(session.created_at) if session else None,
        "revoked": session.revoked if session else None,
        "store": str(ctx.store.root),
        "server_check": None,
    }
    if ctx.args.check and data["logged_in"]:
        try:
            ctx.api().privacy_settings()
            data["server_check"] = "ok"
        except AuthRequired as exc:
            data["logged_in"] = False
            data["server_check"] = str(exc)

    def human(d):
        if d["logged_in"]:
            since = fmt_time(session.created_at) if session else d["since"]
            print(f"Logged in as {d['name'] or d['phone'] or d['user_id']} (user id {d['user_id']}) via {d['method']} since {since}.")
        elif d["revoked"]:
            print(f"Saved login no longer works ({d['revoked']}). Run: hikecli auth --force")
        else:
            print("Not logged in. Run: hikecli auth")
        if d["server_check"]:
            print(f"Server check: {d['server_check']}")
        print(f"Store: {d['store']}")

    ctx.output(data, human)
    return EXIT_OK


def cmd_auth_logout(ctx: Ctx) -> int:
    ctx.require_writable("auth logout")
    session = load_session(ctx.store)
    if session is None:
        ctx.output({"logged_out": False, "server_logout": None}, lambda d: print("Not logged in."))
        return EXIT_OK
    server = "skipped"
    if not ctx.args.local_only and not session.revoked:
        try:
            HikeAPI(ctx.store, session).logout(session.refresh_token)
            server = "ok"
        except HikeError as exc:
            server = f"failed: {exc}"
    delete_session(ctx.store)
    data = {"logged_out": True, "server_logout": server, "local_history": str(ctx.store.db_path)}
    ctx.output(data, lambda d: print(f"Logged out (server: {d['server_logout']}). Local history is still in {d['local_history']}."))
    return EXIT_OK


# ---- sync and history ------------------------------------------------------------


def cmd_sync(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("sync")
    lock = store_lock(ctx, "sync")
    try:
        api = ctx.api()
        out = Emitter(json_mode=ctx.json)
        syncer = Syncer(
            ctx.conn(),
            api,
            api.session.user_id,
            out,
            receipts=not a.no_receipts,
            presence=a.presence,
            max_chats=a.max_chats,
            batch_size=a.batch,
            max_pages=a.pages,
        )
        if a.once:
            counts = syncer.catch_up()
            if not ctx.json:
                print(f"Synced {counts['chats']} chats ({counts['chats_fetched']} fetched), {counts['messages']} new messages, {counts['contacts']} contacts.")
            return EXIT_OK
        try:
            reason = syncer.follow(threading.Event(), idle_exit=a.idle_exit, max_reconnect=a.max_reconnect)
        except KeyboardInterrupt:
            reason = "interrupted"
        out.emit("stopped", reason=reason)
        if not ctx.json:
            status(f"sync stopped ({reason})")
        return EXIT_OK
    finally:
        lock.release()


def cmd_history_backfill(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("history backfill")
    chat_id, name = resolve_chat(ctx, a.chat)
    api = ctx.api()
    syncer = Syncer(ctx.conn(), api, api.session.user_id, Emitter(json_mode=ctx.json, quiet=True), batch_size=a.batch)
    new, done = syncer.backfill_chat(chat_id, a.pages)
    syncer.resolve_names()
    data = {"chat": chat_id, "name": name, "new_messages": new, "complete": done}
    ctx.output(data, lambda d: print(f"{d['name']}: {d['new_messages']} older messages added." + (" Reached the start of the chat." if d["complete"] else " Run again for more.")))
    return EXIT_OK


# ---- chats and messages --------------------------------------------------------------


def cmd_chats_list(ctx: Ctx) -> int:
    a = ctx.args
    chat_type = "group" if a.groups else "one_to_one" if a.direct else None
    rows = db.list_chats(ctx.conn(), unread_only=a.unread, query=a.query, chat_type=chat_type, limit=a.limit)
    data = [chat_dict(r) for r in rows]

    def human(items):
        if not items:
            print("No chats stored yet. Run: hikecli sync --once")
            return
        print_table(
            ["NAME", "TYPE", "UNREAD", "LAST", "PREVIEW", "ID"],
            [[c["name"] or c["id"], "group" if c["type"] == "group" else "direct", c["unread"] or "", fmt_time(c["last_ts"]), c["last_text"], c["id"]] for c in items],
            {0: 28, 4: 50},
        )

    ctx.output(data, human)
    return EXIT_OK


def cmd_chats_mark_read(ctx: Ctx) -> int:
    ctx.require_writable("chats mark-read")
    conn = ctx.conn()
    me = ctx.session().user_id
    chat_id, name = resolve_chat(ctx, ctx.args.chat)
    chat = db.get_chat(conn, chat_id)
    last = conn.execute(
        "SELECT sender_id, server_id FROM messages WHERE chat_id=? AND from_me=0 AND server_id IS NOT NULL ORDER BY ts DESC, id DESC LIMIT 1",
        (chat_id,),
    ).fetchone()
    sender, server_id = (last["sender_id"], last["server_id"]) if last else (None, None)
    if server_id is None and chat is not None and chat["last_server_id"] and chat["last_sender"] not in (None, me):
        sender, server_id = chat["last_sender"], chat["last_server_id"]
    if server_id is None:
        raise CLIError("no received message to mark as read in this chat (run 'hikecli sync --once' first)", code="not_found")
    if chat is not None and chat["api_unread"]:
        packet = build_first_mark_read(me, chat_id, server_id)
    else:
        packet = build_mark_read(me, chat_id, sender, server_id)
    send_packet(ctx, packet)
    db.update_chat(conn, chat_id, unread=0, api_unread=0)
    conn.commit()
    ctx.output({"chat": chat_id, "name": name, "up_to": server_id}, lambda d: print(f"Marked {d['name']} as read."))
    return EXIT_OK


def cmd_messages_list(ctx: Ctx) -> int:
    a = ctx.args
    conn = ctx.conn()
    chat_id, name = resolve_chat(ctx, a.chat)
    me = ctx.me()
    rows = db.list_messages(conn, chat_id, limit=a.limit, before=parse_time(a.before))
    data = [message_dict(conn, r, me, name) for r in rows]

    def human(items):
        if not items:
            print(f"No messages stored for {name}. Run 'hikecli sync --once' or 'hikecli history backfill --chat ...'.")
        for m in items:
            print(message_line(m))

    ctx.output(data, human)
    return EXIT_OK


def cmd_messages_search(ctx: Ctx) -> int:
    a = ctx.args
    conn = ctx.conn()
    chat_id = resolve_chat(ctx, a.chat)[0] if a.chat else None
    me = ctx.me()
    try:
        rows = db.search_messages(conn, a.query, chat_id=chat_id, limit=a.limit)
    except ValueError as exc:
        raise CLIError(str(exc), code="invalid_input") from exc
    except sqlite3.OperationalError as exc:
        raise CLIError(f"search failed: {exc}", code="invalid_input") from exc
    names: dict[str, str] = {}
    data = []
    for r in rows:
        if r["chat_id"] not in names:
            chat = db.get_chat(conn, r["chat_id"])
            names[r["chat_id"]] = (chat["name"] if chat is not None else None) or r["chat_id"]
        data.append(message_dict(conn, r, me, names[r["chat_id"]]))

    def human(items):
        if not items:
            print("No matches.")
        for m in items:
            print(message_line(m, with_chat=True))

    ctx.output(data, human)
    return EXIT_OK


def cmd_messages_show(ctx: Ctx) -> int:
    conn = ctx.conn()
    chat_id, name = resolve_chat(ctx, ctx.args.chat)
    row = db.find_message(conn, chat_id, ctx.args.id)
    if row is None:
        raise CLIError(f"message {ctx.args.id} not found in {name}", code="not_found")
    data = message_dict(conn, row, ctx.me(), name, raw=True)

    def human(m):
        for key in ("id", "server_id", "client_id", "chat_name", "time", "sender_name", "kind", "state", "text", "file_name", "file_size", "mime", "reply_to", "reactions", "local_path"):
            if m.get(key) not in (None, "", {}):
                print(f"{key:>11}: {m[key]}")

    ctx.output(data, human)
    return EXIT_OK


# ---- sending ------------------------------------------------------------------------


def _message_text(value: str) -> str:
    text = sys.stdin.read() if value == "-" else value
    text = text.rstrip("\n") if value == "-" else text
    if not text.strip():
        raise CLIError("message is empty", code="invalid_input")
    return text


def cmd_send_text(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("send text")
    text = _message_text(a.message)
    me = ctx.session().user_id
    chat_id, name = resolve_chat(ctx, a.to, remote=True)
    packet = build_text(me, chat_id, text, reply=reply_info(ctx, chat_id, a.reply_to))
    server_id = send_packet(ctx, packet)
    store_sent(ctx, packet, server_id)
    data = {"to": chat_id, "name": name, "client_id": packet["i"], "server_id": server_id, "ts": packet["ts"]}
    ctx.output(data, lambda d: print(f"Sent to {d['name']} (message id {d['server_id'] or d['client_id']})."))
    return EXIT_OK


def cmd_send_file(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("send file")
    path = Path(a.file).expanduser()
    if not path.is_file():
        raise CLIError(f"file not found: {path}", code="not_found")
    api = ctx.api()
    me = api.session.user_id
    chat_id, name = resolve_chat(ctx, a.to, remote=True)
    reply = reply_info(ctx, chat_id, a.reply_to)
    mime = a.mime or guess_mime(path)
    if not ctx.json:
        status(f"Uploading {path.name} ({path.stat().st_size} bytes) ...")
    file_id = upload(api, path, mime)
    packet = build_file(me, chat_id, file_id=file_id, file_size=path.stat().st_size, mime=mime, file_name=path.name, caption=a.caption, reply=reply)
    server_id = send_packet(ctx, packet)
    store_sent(ctx, packet, server_id)
    data = {"to": chat_id, "name": name, "client_id": packet["i"], "server_id": server_id, "file_id": file_id, "ts": packet["ts"]}
    ctx.output(data, lambda d: print(f"Sent {path.name} to {d['name']} (message id {d['server_id'] or d['client_id']})."))
    return EXIT_OK


# ---- contacts, groups, media -----------------------------------------------------------


def cmd_contacts_list(ctx: Ctx) -> int:
    rows = db.list_contacts(ctx.conn(), query=ctx.args.query, limit=ctx.args.limit)
    data = [{"user_id": r["user_id"], "name": r["name"], "phone": r["phone"], "status": r["status"], "in_address_book": bool(r["in_address_book"])} for r in rows]

    def human(items):
        if not items:
            print("No contacts stored yet. Run: hikecli contacts refresh")
            return
        print_table(["NAME", "PHONE", "SAVED", "USER ID"], [[c["name"] or "", c["phone"] or "", "yes" if c["in_address_book"] else "", c["user_id"]] for c in items], {0: 30})

    ctx.output(data, human)
    return EXIT_OK


def cmd_contacts_refresh(ctx: Ctx) -> int:
    ctx.require_writable("contacts refresh")
    api = ctx.api()
    syncer = Syncer(ctx.conn(), api, api.session.user_id, Emitter(quiet=True))
    count = syncer.sync_contacts()
    named = syncer.resolve_names()
    ctx.output({"contacts": count, "profiles": named}, lambda d: print(f"Updated {d['contacts']} contacts and {d['profiles']} profile names."))
    return EXIT_OK


def cmd_groups_list(ctx: Ctx) -> int:
    rows = db.list_chats(ctx.conn(), chat_type="group", limit=ctx.args.limit)
    data = [chat_dict(r) for r in rows]

    def human(items):
        if not items:
            print("No groups stored yet. Run: hikecli sync --once")
            return
        print_table(["NAME", "UNREAD", "LAST", "ID"], [[g["name"] or g["id"], g["unread"] or "", fmt_time(g["last_ts"]), g["id"]] for g in items], {0: 40})

    ctx.output(data, human)
    return EXIT_OK


def cmd_groups_info(ctx: Ctx) -> int:
    conn = ctx.conn()
    group_id, name = resolve_chat(ctx, ctx.args.group)
    info = ctx.api().group(group_id)
    members = info.get("members") or []
    if not ctx.read_only:
        if info.get("name"):
            db.update_chat(conn, group_id, type="group", name=info["name"])
        db.replace_group_members(conn, group_id, members)
        conn.commit()
    data = {
        "id": group_id,
        "name": info.get("name") or name,
        "description": info.get("description"),
        "created_by": info.get("createdBy"),
        "created_at": info.get("createdAt"),
        "my_permissions": info.get("myPermissions"),
        "members": [{"user_id": m.get("userId"), "name": m.get("name"), "phone": m.get("phoneNumber"), "role": m.get("role")} for m in members],
    }

    def human(d):
        print(f"{d['name']}  ({d['id']})")
        if d["description"]:
            print(d["description"])
        print_table(["MEMBER", "PHONE", "ROLE", "USER ID"], [[m["name"] or "", m["phone"] or "", (m["role"] or "").lower(), m["user_id"]] for m in d["members"]])

    ctx.output(data, human)
    return EXIT_OK


def cmd_media_download(ctx: Ctx) -> int:
    a = ctx.args
    ctx.require_writable("media download")
    conn = ctx.conn()
    chat_id, name = resolve_chat(ctx, a.chat)
    row = db.find_message(conn, chat_id, a.id)
    if row is None:
        raise CLIError(f"message {a.id} not found in {name}", code="not_found")
    if not row["file_id"]:
        raise CLIError("that message has no downloadable media", code="no_media")
    file_name = safe_filename(row["file_name"] or row["file_id"])
    if not Path(file_name).suffix and row["mime"]:
        import mimetypes

        file_name += mimetypes.guess_extension(row["mime"]) or ""
    if a.out:
        dest = Path(a.out).expanduser()
        if dest.is_dir():
            dest = dest / file_name
    else:
        dest = ctx.store.media_dir / safe_filename(chat_id) / file_name
    result = download(ctx.api(), row["file_id"], dest)
    conn.execute("UPDATE messages SET local_path=? WHERE id=?", (result["path"], row["id"]))
    conn.commit()
    ctx.output(result, lambda d: print(f"Saved {d['size']} bytes to {d['path']}"))
    return EXIT_OK


# ---- doctor ----------------------------------------------------------------------------


def cmd_doctor(ctx: Ctx) -> int:
    checks: list[dict] = []

    def check(name: str, ok: bool, detail: str) -> None:
        checks.append({"check": name, "ok": ok, "detail": detail})

    check("python", sys.version_info >= (3, 10), sys.version.split()[0])
    check("sqlite fts5", db.fts5_available(), f"SQLite {sqlite3.sqlite_version}")
    try:
        ctx.store.ensure()
        probe = ctx.store.root / ".doctor"
        probe.write_text("ok", encoding="ascii")
        probe.unlink()
        check("store", True, str(ctx.store.root))
    except OSError as exc:
        check("store", False, f"{ctx.store.root}: {exc}")
    try:
        stats = db.stats(ctx.conn())
        check("database", True, f"{stats['chats']} chats, {stats['messages']} messages, {stats['contacts']} contacts")
    except sqlite3.Error as exc:
        check("database", False, str(exc))
    session = load_session(ctx.store)
    if session is None:
        check("login", False, "not logged in (run: hikecli auth)")
    elif session.revoked:
        check("login", False, f"saved login no longer works: {session.revoked}")
    else:
        check("login", True, f"{session.display_name} ({session.user_id}) via {session.login_method}")
    check("endpoints", True, f"api={api_base()} login={session_ws_base()} relay={mqtt_ws_url()}")
    if ctx.args.connect and session is not None and not session.revoked:
        try:
            ctx.api().privacy_settings()
            check("api", True, "authenticated request ok")
        except HikeError as exc:
            check("api", False, str(exc))
        try:
            client = open_mqtt(ctx.api())
            try:
                client.subscribe(f"u:{ctx.api().session.user_id}", qos=1)
                check("relay", True, "MQTT login and subscribe ok")
            finally:
                client.disconnect()
        except (MQTTError, HikeError) as exc:
            check("relay", False, str(exc))
    ok = all(c["ok"] for c in checks)

    def human(d):
        for c in d["checks"]:
            print(f"[{'ok' if c['ok'] else 'FAIL':>4}] {c['check']}: {c['detail']}")

    ctx.output({"ok": ok, "checks": checks}, human)
    return EXIT_OK if ok else EXIT_ERROR


# ---- parser -------------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--store", default=argparse.SUPPRESS, help="store directory (default: ~/.hikecli, or $HIKECLI_STORE)")
    common.add_argument("--json", action="store_true", default=argparse.SUPPRESS, help="print JSON ({success,data,error}); sync prints NDJSON events")
    common.add_argument("--read-only", action="store_true", default=argparse.SUPPRESS, help="refuse anything that changes Hike or the local store (or set HIKECLI_READONLY=1)")

    parser = argparse.ArgumentParser(
        prog="hikecli",
        parents=[common],
        description="Unofficial Hike Messenger CLI: links as a Hike Web device, keeps a searchable local copy of your chats, and sends messages.",
    )
    parser.add_argument("--version", action="version", version=f"hikecli {__version__}")
    sub = parser.add_subparsers(dest="command", metavar="<command>")

    def add(container, name: str, func, help_text: str):
        p = container.add_parser(name, parents=[common], help=help_text, description=help_text)
        p.set_defaults(func=func)
        return p

    auth = add(sub, "auth", cmd_auth, "log in by scanning a QR code with the Hike app (or --phone for an SMS code)")
    auth.add_argument("--phone", help="log in with an SMS OTP instead of a QR code, e.g. +919876543210")
    auth.add_argument("--qr-format", choices=["terminal", "text", "png"], default="terminal", help="how to show the QR code")
    auth.add_argument("--timeout", type=float, default=300, help="seconds to wait for the scan (default 300)")
    auth.add_argument("--no-sync", action="store_true", help="do not sync after logging in")
    auth.add_argument("--force", action="store_true", help="log in again even if a login is saved")
    auth.add_argument("--max-chats", type=int, default=50, help="chats to fetch messages for after login (0 = all)")
    auth.add_argument("--batch", type=int, default=50, help="messages per history request")
    auth_sub = auth.add_subparsers(dest="auth_command", metavar="[status|logout]")
    st = add(auth_sub, "status", cmd_auth_status, "show who is logged in")
    st.add_argument("--check", action="store_true", help="also ask Hike whether the login still works")
    lo = add(auth_sub, "logout", cmd_auth_logout, "log this device out and forget the saved login")
    lo.add_argument("--local-only", action="store_true", help="only delete the saved login; do not tell Hike")

    sy = add(sub, "sync", cmd_sync, "catch up on chats and keep listening for new messages")
    sy.add_argument("--once", action="store_true", help="catch up once over HTTP and exit")
    sy.add_argument("--presence", choices=["quiet", "normal"], default="quiet", help="quiet = act like a background tab (default); normal = like an open tab")
    sy.add_argument("--no-receipts", action="store_true", help="do not send delivery receipts for messages fetched while offline")
    sy.add_argument("--idle-exit", type=float, default=None, help="exit after this many seconds without new events")
    sy.add_argument("--max-reconnect", type=float, default=None, help="give up after being offline this many seconds (default: retry forever)")
    sy.add_argument("--max-chats", type=int, default=50, help="chats to fetch messages for per catch-up (0 = all)")
    sy.add_argument("--batch", type=int, default=50, help="messages per history request")
    sy.add_argument("--pages", type=int, default=3, help="max history pages per chat when catching up")

    chats = sub.add_parser("chats", parents=[common], help="list chats, mark them read")
    chats_sub = chats.add_subparsers(dest="chats_command", metavar="<list|mark-read>", required=True)
    cl = add(chats_sub, "list", cmd_chats_list, "list chats, most recent first")
    cl.add_argument("--unread", action="store_true", help="only chats with unread messages")
    cl.add_argument("--groups", action="store_true", help="only groups")
    cl.add_argument("--direct", action="store_true", help="only one-to-one chats")
    cl.add_argument("--query", help="filter by name, phone or id")
    cl.add_argument("--limit", type=int, default=50)
    mr = add(chats_sub, "mark-read", cmd_chats_mark_read, "mark a chat as read (sends a read receipt)")
    mr.add_argument("--chat", required=True, help="chat name, phone number or id")

    msgs = sub.add_parser("messages", parents=[common], help="read and search stored messages")
    msgs_sub = msgs.add_subparsers(dest="messages_command", metavar="<list|search|show>", required=True)
    ml = add(msgs_sub, "list", cmd_messages_list, "show the latest messages in a chat (oldest first)")
    ml.add_argument("--chat", required=True, help="chat name, phone number or id")
    ml.add_argument("--limit", type=int, default=50)
    ml.add_argument("--before", help="only messages before this time (YYYY-MM-DD[ HH:MM] or epoch ms)")
    ms = add(msgs_sub, "search", cmd_messages_search, "full-text search across stored messages")
    ms.add_argument("query", help="words to find; end a word with * for prefix matches")
    ms.add_argument("--chat", help="limit to one chat")
    ms.add_argument("--limit", type=int, default=50)
    mshow = add(msgs_sub, "show", cmd_messages_show, "show one message with all details")
    mshow.add_argument("--chat", required=True)
    mshow.add_argument("--id", required=True, help="message id (server or client id)")

    hist = sub.add_parser("history", parents=[common], help="fetch older messages")
    hist_sub = hist.add_subparsers(dest="history_command", metavar="<backfill>", required=True)
    hb = add(hist_sub, "backfill", cmd_history_backfill, "fetch older messages for one chat from Hike's server")
    hb.add_argument("--chat", required=True)
    hb.add_argument("--pages", type=int, default=5, help="pages to fetch this run (default 5)")
    hb.add_argument("--batch", type=int, default=50, help="messages per page")

    send = sub.add_parser("send", parents=[common], help="send messages and files")
    send_sub = send.add_subparsers(dest="send_command", metavar="<text|file>", required=True)
    stext = add(send_sub, "text", cmd_send_text, "send a text message")
    stext.add_argument("--to", required=True, help="name, phone number (+country code) or id")
    stext.add_argument("--message", required=True, help="text to send; use - to read it from stdin")
    stext.add_argument("--reply-to", help="message id to reply to")
    sfile = add(send_sub, "file", cmd_send_file, "send a file as a document")
    sfile.add_argument("--to", required=True, help="name, phone number (+country code) or id")
    sfile.add_argument("--file", required=True, help="path of the file to send")
    sfile.add_argument("--caption", help="optional caption")
    sfile.add_argument("--mime", help="override the detected MIME type")
    sfile.add_argument("--reply-to", help="message id to reply to")

    contacts = sub.add_parser("contacts", parents=[common], help="list or refresh contacts")
    contacts_sub = contacts.add_subparsers(dest="contacts_command", metavar="<list|refresh>", required=True)
    ctl = add(contacts_sub, "list", cmd_contacts_list, "list stored contacts")
    ctl.add_argument("--query", help="filter by name, phone or id")
    ctl.add_argument("--limit", type=int, default=200)
    add(contacts_sub, "refresh", cmd_contacts_refresh, "download contacts and profile names from Hike")

    groups = sub.add_parser("groups", parents=[common], help="list groups, show members")
    groups_sub = groups.add_subparsers(dest="groups_command", metavar="<list|info>", required=True)
    gl = add(groups_sub, "list", cmd_groups_list, "list groups")
    gl.add_argument("--limit", type=int, default=100)
    gi = add(groups_sub, "info", cmd_groups_info, "show a group's details and members (live from Hike)")
    gi.add_argument("--group", required=True, help="group name or id")

    media = sub.add_parser("media", parents=[common], help="download attachments")
    media_sub = media.add_subparsers(dest="media_command", metavar="<download>", required=True)
    md = add(media_sub, "download", cmd_media_download, "download a message's photo, video, audio or file")
    md.add_argument("--chat", required=True)
    md.add_argument("--id", required=True, help="message id")
    md.add_argument("--out", help="file or folder to save to (default: <store>/media/<chat>/)")

    doc = add(sub, "doctor", cmd_doctor, "check the install, store and login")
    doc.add_argument("--connect", action="store_true", help="also test the live API and message relay")
    return parser


def _configure_stdio() -> None:
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass


def main(argv: list[str] | None = None) -> int:
    _configure_stdio()
    parser = build_parser()
    args = parser.parse_args(argv)
    if not getattr(args, "func", None):
        parser.print_help()
        return EXIT_USAGE
    ctx = Ctx(args)

    def fail(exc: Exception, code: str, exit_code: int) -> int:
        if ctx.json:
            print(json.dumps({"success": False, "data": None, "error": {"code": code, "message": str(exc)}}, ensure_ascii=False))
        else:
            print(f"error: {exc}", file=sys.stderr)
        return exit_code

    try:
        return args.func(ctx) or EXIT_OK
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return 130
    except AuthRequired as exc:
        return fail(exc, "auth_required", EXIT_AUTH)
    except CLIError as exc:
        return fail(exc, exc.code, exc.exit_code)
    except LoginError as exc:
        return fail(exc, "login_failed", EXIT_ERROR)
    except HikeError as exc:
        return fail(exc, exc.code or "hike_error", EXIT_ERROR)
    except MQTTError as exc:
        return fail(exc, "relay_error", EXIT_ERROR)
    finally:
        ctx.close()
