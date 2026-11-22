"""History catch-up over REST and live updates over MQTT."""

from __future__ import annotations

import json
import queue
import sqlite3
import sys
import threading
import time
from datetime import datetime

from . import db
from .api import AuthRequired, HikeAPI, HikeError
from .config import USER_AGENT, WEB_ORIGIN, mqtt_ws_url
from .mqtt import MQTTAuthError, MQTTClient, MQTTDisconnected, MQTTError, MQTTTimeout
from .protocol import (
    CA_ARCHIVE,
    CA_BLOCK,
    CA_CLEAR,
    CA_DELETE,
    CA_MUTE,
    CA_PIN,
    CA_UNREAD,
    MA_DELETE_FOR_ALL,
    MA_EDIT,
    ST_ADD_REACTION,
    ST_REMOVE_REACTION,
    T_CALL,
    T_CONVERSATION_ACTION,
    T_DATA_SYNC,
    T_DELIVERY,
    T_END_TYPING,
    T_FIRST_DELIVERY,
    T_FIRST_MARK_READ,
    T_GROUP_ACTION,
    T_INVITE,
    T_MARK_READ,
    T_MESSAGE,
    T_MESSAGE_ACTION,
    T_PRESENCE,
    T_REACTION,
    T_START_TYPING,
    Message,
    as_int,
    as_str,
    build_first_delivery,
    build_presence,
    conversation_id,
    now_ms,
    packet_from_history_item,
    parse_message,
    server_seq,
    summary,
)


def fmt_time(ms: int | None) -> str:
    if not ms:
        return ""
    return datetime.fromtimestamp(ms / 1000).strftime("%Y-%m-%d %H:%M")


def open_mqtt(api: HikeAPI) -> MQTTClient:
    """Connect like Hike Web: clientId web_<userId>_<ms>, username userId, password access token."""
    for attempt in range(2):
        session = api.session
        client = MQTTClient(
            mqtt_ws_url(),
            client_id=f"web_{session.user_id}_{now_ms()}",
            username=session.user_id,
            password=session.access_token,
            keepalive=25,
            origin=WEB_ORIGIN,
            headers=[f"User-Agent: {USER_AGENT}"],
        )
        try:
            client.connect()
            return client
        except MQTTAuthError:
            if attempt:
                raise AuthRequired("Hike's message relay rejected the login even after a token refresh. Run: hikecli auth")
            api.refresh()
    raise AssertionError("unreachable")


def publish_packet(client: MQTTClient, me: str, packet: dict, *, qos: int = 1, attempts: int = 3, ack_timeout: float = 5.0) -> str | None:
    """Publish to the user's own topic (as Hike Web does) and return the server id from the PUBACK.

    Hike Web retries unacknowledged publishes with the same client id; so do we.
    """
    payload = json.dumps(packet, ensure_ascii=False, separators=(",", ":"))
    if qos == 0:
        client.publish(me, payload, qos=0)
        return None
    for attempt in range(attempts):
        try:
            ack = client.publish(me, payload, qos=1, timeout=ack_timeout)
            return ack.decode("utf-8", "replace") if ack else None
        except MQTTTimeout:
            if attempt == attempts - 1:
                raise MQTTTimeout(
                    f"Hike did not confirm delivery after {attempts} attempts; the message may or may not have been sent"
                )
            time.sleep(min(4.0, 2.0**attempt))
    return None


class Emitter:
    """Human lines (stdout for messages, stderr for status) or NDJSON events on stdout."""

    def __init__(self, json_mode: bool = False, quiet: bool = False):
        self.json_mode = json_mode
        self.quiet = quiet

    def emit(self, event: str, /, **data) -> None:
        if self.json_mode and not self.quiet:
            print(json.dumps({"event": event, **data}, ensure_ascii=False, default=str), flush=True)

    def line(self, human: str, event: str, /, **data) -> None:
        if self.json_mode:
            self.emit(event, **data)
        elif not self.quiet:
            print(human, flush=True)

    def status(self, text: str) -> None:
        if self.json_mode:
            self.emit("status", message=text)
        else:
            print(text, file=sys.stderr, flush=True)


class Syncer:
    def __init__(
        self,
        conn: sqlite3.Connection,
        api: HikeAPI,
        me: str,
        out: Emitter,
        *,
        receipts: bool = True,
        presence: str = "quiet",
        max_chats: int = 50,
        batch_size: int = 50,
        max_pages: int = 3,
    ):
        self.conn = conn
        self.api = api
        self.me = me
        self.out = out
        self.receipts = receipts
        self.presence = presence
        self.max_chats = max_chats
        self.batch_size = batch_size
        self.max_pages = max(1, max_pages)
        self.client: MQTTClient | None = None
        self._groups_fetched: set[str] = set()

    # ---- REST ------------------------------------------------------------

    def sync_contacts(self) -> int:
        synced_at = int(db.get_meta(self.conn, "contacts_synced_at") or 0)
        cursor, count, latest = None, 0, synced_at
        while True:
            contacts, cursor, server_time = self.api.contacts_page(synced_at, cursor)
            for c in contacts:
                uid = as_str(c.get("userId"))
                if not uid:
                    continue
                if c.get("isDeleted"):
                    db.upsert_contact(self.conn, uid, in_address_book=False)
                else:
                    db.upsert_contact(
                        self.conn,
                        uid,
                        name=as_str(c.get("nickName")),
                        phone=as_str(c.get("phoneNumber")),
                        status=as_str(c.get("status")),
                        pic=as_str(c.get("pic")),
                        in_address_book=True,
                    )
                count += 1
            latest = as_int(server_time) or latest
            if not cursor:
                break
        db.set_meta(self.conn, "contacts_synced_at", latest)
        self.conn.commit()
        return count

    def sync_conversations(self) -> list[dict]:
        chats = []
        for item in self.api.conversations():
            cid = as_str(item.get("conversationId"))
            if not cid or cid == self.me:
                continue
            chat_type = "one_to_one" if item.get("conversationType") == "ONE_ON_ONE" else "group"
            phone = as_str(item.get("phoneNumber"))
            name = as_str(item.get("conversationName")) or phone
            unread = as_int(item.get("unreadCount")) or 0
            fields = dict(
                type=chat_type,
                unread=unread,
                api_unread=int(unread > 0),
                undelivered=int(bool(item.get("undelivered"))),
                archived=int(bool(item.get("archived"))),
                muted=int(bool(item.get("muted"))),
                blocked=int(bool(item.get("blocked"))),
                pinned=int(bool(item.get("pinned"))),
            )
            if name:
                fields["name"] = name
            if phone:
                fields["phone"] = phone
            last_ts = as_int(item.get("lastMessageTs"))
            last_sender = as_str(item.get("lastMessageSentBy"))
            last_server_id = as_str(item.get("lastMessageId"))
            packet = item.get("lastMessage")
            if isinstance(packet, str):
                try:
                    packet = json.loads(packet)
                except ValueError:
                    packet = None
            if isinstance(packet, dict):
                msg = parse_message(packet, self.me, chat_id=cid)
                if msg:
                    deleted = bool(item.get("lastMessageIsDeletedForAll"))
                    fields["last_text"] = summary(msg.kind, None if deleted else msg.text, msg.file_name, deleted)
                    last_server_id = last_server_id or msg.server_id
                    last_sender = last_sender or msg.sender_id
            if server_seq(last_server_id) is None:
                last_server_id = None
            if last_ts:
                fields.update(last_ts=last_ts, last_sender=last_sender)
                if last_server_id:
                    fields["last_server_id"] = last_server_id
            db.update_chat(self.conn, cid, **fields)
            if chat_type == "one_to_one" and (name or phone):
                db.upsert_contact(self.conn, cid, name=name if name != phone else None, phone=phone, overwrite_name=False)
            chats.append(
                {"id": cid, "type": chat_type, "last_ts": last_ts, "last_sender": last_sender, "last_server_id": last_server_id, "undelivered": bool(item.get("undelivered"))}
            )
        self.conn.commit()
        return chats

    def _learn_sender(self, msg: Message) -> None:
        if not msg.from_me and msg.sender_id and (msg.sender_name or msg.sender_phone):
            db.upsert_contact(self.conn, msg.sender_id, name=msg.sender_name, phone=msg.sender_phone, overwrite_name=False)

    def _store_history(self, chat_id: str, items: list[dict]) -> tuple[int, int]:
        new = old = 0
        for item in items:
            packet = packet_from_history_item(item)
            msg = parse_message(packet, self.me, meta=item, chat_id=chat_id) if packet else None
            if msg is None:
                continue
            _, inserted = db.upsert_message(self.conn, msg)
            self._learn_sender(msg)
            db.touch_chat_last(self.conn, msg)
            if inserted:
                new += 1
            else:
                old += 1
        return new, old

    def _initialized(self, chat_id: str) -> bool:
        row = db.get_chat(self.conn, chat_id)
        if row is not None and (row["history_cursor"] or row["history_done"]):
            return True
        return db.chat_last_local_ts(self.conn, chat_id) is not None

    def catch_up_chat(self, chat_id: str) -> int:
        """Fetch the newest pages until they overlap what we already have."""
        first_time = not self._initialized(chat_id)
        cursor, total = None, 0
        for _ in range(1 if first_time else self.max_pages):
            items, next_cursor = self.api.messages(chat_id, self.batch_size, cursor)
            new, old = self._store_history(chat_id, items)
            total += new
            if first_time:
                db.update_chat(self.conn, chat_id, history_cursor=next_cursor, history_done=int(next_cursor is None))
            self.conn.commit()
            if old or not next_cursor:
                break
            cursor = next_cursor
        return total

    def backfill_chat(self, chat_id: str, pages: int) -> tuple[int, bool]:
        """Walk older history from the saved cursor. Returns (new messages, reached the beginning)."""
        row = db.get_chat(self.conn, chat_id)
        if row is not None and row["history_done"]:
            return 0, True
        cursor = row["history_cursor"] if row is not None else None
        total = 0
        for _ in range(max(1, pages)):
            items, next_cursor = self.api.messages(chat_id, self.batch_size, cursor)
            new, _ = self._store_history(chat_id, items)
            total += new
            db.update_chat(self.conn, chat_id, history_cursor=next_cursor, history_done=int(next_cursor is None))
            self.conn.commit()
            if not next_cursor:
                return total, True
            cursor = next_cursor
        return total, False

    def resolve_names(self) -> int:
        ids = db.unnamed_senders(self.conn, self.me)
        for row in self.conn.execute("SELECT id FROM chats WHERE type='one_to_one' AND (name IS NULL OR name = id)"):
            if row["id"] not in ids:
                ids.append(row["id"])
        if not ids:
            return 0
        try:
            profiles = self.api.profiles(ids[:200])
        except AuthRequired:
            raise
        except HikeError as exc:
            self.out.status(f"could not look up {len(ids)} profile names: {exc}")
            return 0
        for profile in profiles:
            uid = as_str(profile.get("userId"))
            if not uid:
                continue
            name, phone = as_str(profile.get("name")), as_str(profile.get("phone"))
            db.upsert_contact(
                self.conn, uid, name=name, phone=phone, status=as_str(profile.get("statusMsg")), pic=as_str(profile.get("image")), overwrite_name=False
            )
            chat = db.get_chat(self.conn, uid)
            if chat is not None and (not chat["name"] or chat["name"] == uid) and (name or phone):
                db.update_chat(self.conn, uid, name=name or phone)
        self.conn.commit()
        return len(profiles)

    def _is_stale(self, chat: dict) -> bool:
        if not chat["last_ts"]:
            return False
        if chat["last_server_id"] and db.find_message(self.conn, chat["id"], chat["last_server_id"]) is not None:
            return False
        local = db.chat_last_local_ts(self.conn, chat["id"])
        return local is None or chat["last_ts"] > local

    def catch_up(self, *, contacts: bool = True) -> dict:
        counts = {"contacts": 0, "chats": 0, "chats_fetched": 0, "messages": 0, "receipts": 0}
        if contacts:
            try:
                counts["contacts"] = self.sync_contacts()
            except AuthRequired:
                raise
            except HikeError as exc:
                self.out.status(f"contacts sync failed: {exc}")
        chats = self.sync_conversations()
        counts["chats"] = len(chats)
        stale = sorted((c for c in chats if self._is_stale(c)), key=lambda c: c["last_ts"] or 0, reverse=True)
        skipped = 0
        if self.max_chats and len(stale) > self.max_chats:
            skipped = len(stale) - self.max_chats
            stale = stale[: self.max_chats]
        for chat in stale:
            counts["messages"] += self.catch_up_chat(chat["id"])
            counts["chats_fetched"] += 1
        if skipped:
            self.out.status(f"{skipped} older chats were not fetched this time (use --max-chats 0, or 'hikecli history backfill --chat ...')")
        self.resolve_names()
        if self.receipts and self.client is not None:
            counts["receipts"] = self.send_first_deliveries(chats)
        self.out.emit("sync", **counts)
        return counts

    def send_first_deliveries(self, chats: list[dict]) -> int:
        """Tell senders their messages reached this device (Hike Web does this on load)."""
        sent = 0
        for chat in chats:
            if not (chat["undelivered"] and chat["last_server_id"] and chat["last_sender"] and chat["last_sender"] != self.me):
                continue
            try:
                publish_packet(self.client, self.me, build_first_delivery(self.me, chat["id"], chat["last_server_id"]))
            except MQTTError as exc:
                self.out.status(f"could not send delivery receipts: {exc}")
                break
            db.update_chat(self.conn, chat["id"], undelivered=0)
            sent += 1
        self.conn.commit()
        return sent

    # ---- live --------------------------------------------------------------

    def handle_packet(self, packet: dict) -> None:
        kind = packet.get("t")
        if kind in (T_MESSAGE, T_INVITE, T_CALL, T_GROUP_ACTION):
            self._on_message(packet)
        elif kind in (T_DELIVERY, T_FIRST_DELIVERY):
            self._on_receipt(packet, "delivered")
        elif kind in (T_MARK_READ, T_FIRST_MARK_READ):
            self._on_receipt(packet, "read")
        elif kind == T_REACTION:
            self._on_reaction(packet)
        elif kind == T_MESSAGE_ACTION:
            self._on_message_action(packet)
        elif kind == T_CONVERSATION_ACTION:
            self._on_conversation_action(packet)
        elif kind in (T_START_TYPING, T_END_TYPING):
            sender = as_str(packet.get("f"))
            if sender != self.me:
                self.out.emit("typing", chat=conversation_id(packet, self.me), user=sender, typing=kind == T_START_TYPING)
        elif kind == T_PRESENCE:
            data = packet.get("d") or {}
            self.out.emit("presence", user=as_str(data.get("uid")), last_seen=as_int(data.get("pr")))
        elif kind == T_DATA_SYNC:
            self.out.emit("data_sync", kind=(packet.get("d") or {}).get("st"))
        else:
            self.out.emit("packet", type=kind)
        self.conn.commit()

    def _chat_name(self, chat_id: str) -> str:
        row = db.get_chat(self.conn, chat_id)
        return (row["name"] if row is not None else None) or db.display_name(self.conn, chat_id)

    def _fetch_group(self, group_id: str) -> None:
        if group_id in self._groups_fetched:
            return
        self._groups_fetched.add(group_id)
        try:
            info = self.api.group(group_id)
        except AuthRequired:
            raise
        except HikeError:
            return
        if info.get("name"):
            db.update_chat(self.conn, group_id, type="group", name=info["name"])
        db.replace_group_members(self.conn, group_id, info.get("members") or [])

    def _on_message(self, packet: dict) -> None:
        msg = parse_message(packet, self.me)
        if msg is None:
            return
        target = as_str(packet.get("to"))
        is_group = packet.get("t") == T_GROUP_ACTION or (not msg.from_me and target not in (self.me, f"u:{self.me}"))
        existing = db.get_chat(self.conn, msg.chat_id)
        if existing is None:
            db.ensure_chat(self.conn, msg.chat_id, "group" if is_group else "one_to_one")
            if is_group:
                self._fetch_group(msg.chat_id)
            elif not msg.from_me and (msg.sender_name or msg.sender_phone):
                db.update_chat(self.conn, msg.chat_id, name=msg.sender_name or msg.sender_phone, phone=msg.sender_phone)
        _, inserted = db.upsert_message(self.conn, msg)
        self._learn_sender(msg)
        db.touch_chat_last(self.conn, msg)
        if inserted and not msg.from_me and msg.kind not in ("call", "system"):
            self.conn.execute("UPDATE chats SET unread = unread + 1 WHERE id=?", (msg.chat_id,))
        self.conn.commit()
        if inserted:
            self._print_message(msg)

    def _print_message(self, msg: Message) -> None:
        chat = db.get_chat(self.conn, msg.chat_id)
        chat_name = self._chat_name(msg.chat_id)
        sender = "me" if msg.from_me else (db.display_name(self.conn, msg.sender_id) or msg.sender_name or msg.sender_id)
        text = summary(msg.kind, msg.text, msg.file_name, bool(msg.deleted_for_all))
        if chat is not None and chat["type"] == "group":
            line = f"{fmt_time(msg.ts)}  {chat_name} / {sender}: {text}"
        elif msg.from_me:
            line = f"{fmt_time(msg.ts)}  me -> {chat_name}: {text}"
        else:
            line = f"{fmt_time(msg.ts)}  {sender}: {text}"
        self.out.line(
            line,
            "message",
            chat=msg.chat_id,
            chat_name=chat_name,
            id=msg.id,
            server_id=msg.server_id,
            client_id=msg.client_id,
            sender=msg.sender_id,
            sender_name=sender,
            from_me=msg.from_me,
            ts=msg.ts,
            kind=msg.kind,
            text=msg.text,
            file_name=msg.file_name,
            reply_to=msg.reply_to_id,
        )

    def _on_receipt(self, packet: dict, state: str) -> None:
        chat = conversation_id(packet, self.me)
        sender = as_str(packet.get("f"))
        if not chat:
            return
        if sender == self.me:
            if state == "read":  # read on another of my devices
                db.update_chat(self.conn, chat, unread=0, api_unread=0)
            return
        data = packet.get("d")
        server_id = None
        if isinstance(data, dict):
            server_id = as_str(data.get(self.me)) or as_str(data.get("s"))
            if server_id is None and len(data) == 1:
                server_id = as_str(next(iter(data.values())))
        elif data is not None:
            server_id = as_str(data)
        server_id = server_id or as_str(packet.get("s"))
        seq = server_seq(server_id)
        row = db.get_chat(self.conn, chat)
        if seq is None or (row is not None and row["type"] == "group"):
            return  # group receipts are per member; Hike Web tracks them separately
        updated = db.mark_my_messages(self.conn, chat, seq, state)
        self.out.emit("receipt", chat=chat, by=sender, state=state, up_to=server_id, updated=updated)

    def _on_reaction(self, packet: dict) -> None:
        chat = conversation_id(packet, self.me)
        data = packet.get("d") or {}
        target, sender = as_str(data.get("sid")), as_str(packet.get("f"))
        if not (chat and target and sender) or data.get("st") not in (ST_ADD_REACTION, ST_REMOVE_REACTION):
            return
        emoji = as_str(data.get("r")) if data.get("st") == ST_ADD_REACTION else None
        db.set_reaction(self.conn, chat, target, sender, emoji, as_int(packet.get("ts")) or now_ms())
        who = "me" if sender == self.me else db.display_name(self.conn, sender)
        line = f"{fmt_time(as_int(packet.get('ts')))}  {who} reacted {emoji} in {self._chat_name(chat)}" if emoji else ""
        if line or self.out.json_mode:
            self.out.line(line, "reaction", chat=chat, target=target, user=sender, emoji=emoji)

    def _on_message_action(self, packet: dict) -> None:
        chat = conversation_id(packet, self.me)
        data = packet.get("d") or {}
        action, target = data.get("st"), as_str(data.get("sid"))
        if not chat or not target:
            return
        sender = as_str(packet.get("f"))
        changed = db.apply_message_action(
            self.conn, chat, action, target, text=data.get("m"), value=data.get("val"), by_me=sender == self.me
        )
        if action == MA_EDIT:
            self.out.line(f"{fmt_time(as_int(packet.get('ts')))}  message edited in {self._chat_name(chat)}: {data.get('m')}", "edit", chat=chat, target=target, text=data.get("m"), updated=changed)
        elif action == MA_DELETE_FOR_ALL:
            self.out.line(f"{fmt_time(as_int(packet.get('ts')))}  message deleted in {self._chat_name(chat)}", "delete", chat=chat, target=target, updated=changed)
        else:
            self.out.emit("message_action", chat=chat, action=action, target=target, value=data.get("val"), updated=changed)

    def _on_conversation_action(self, packet: dict) -> None:
        chat = conversation_id(packet, self.me)
        data = packet.get("d") or {}
        action, value = data.get("st"), bool(data.get("val"))
        if not chat:
            return
        column = {CA_ARCHIVE: "archived", CA_MUTE: "muted", CA_PIN: "pinned", CA_BLOCK: "blocked"}.get(action)
        if column:
            db.update_chat(self.conn, chat, **{column: int(value)})
        elif action == CA_UNREAD:
            if value:
                self.conn.execute("UPDATE chats SET unread = MAX(unread, 1) WHERE id=?", (chat,))
            else:
                db.update_chat(self.conn, chat, unread=0, api_unread=0)
        elif action in (CA_CLEAR, CA_DELETE):
            cutoff = as_int(data.get("ts")) or as_int(packet.get("ts")) or now_ms()
            self.conn.execute("UPDATE messages SET deleted_for_me=1 WHERE chat_id=? AND ts <= ?", (chat, cutoff))
        self.out.emit("chat_action", chat=chat, action=action, value=value)

    def follow(self, stop: threading.Event, *, idle_exit: float | None = None, max_reconnect: float | None = None) -> str:
        """Stay connected, store everything that arrives, reconnect on drops. Returns why it stopped."""
        backoff, down_since, first = 1.0, None, True
        while not stop.is_set():
            try:
                client = open_mqtt(self.api)
            except AuthRequired:
                raise
            except (MQTTError, HikeError) as exc:
                down_since = down_since or time.monotonic()
                if max_reconnect is not None and time.monotonic() - down_since >= max_reconnect:
                    raise
                self.out.status(f"cannot connect to Hike ({exc}); retrying in {backoff:.0f}s")
                stop.wait(backoff)
                backoff = min(backoff * 2, 60.0)
                continue
            self.client, down_since = client, None
            try:
                client.subscribe(f"u:{self.me}", qos=1)
                publish_packet(client, self.me, build_presence(self.me, self.presence == "normal"), qos=0)
                self.out.emit("connected")
                counts = self.catch_up(contacts=first)
                first, backoff = False, 1.0
                if not self.out.json_mode:
                    self.out.status(f"caught up: {counts['messages']} new messages in {counts['chats_fetched']} chats")
                self.out.status("listening for new messages (Ctrl+C to stop)")
                last_activity = time.monotonic()
                while not stop.is_set():
                    try:
                        item = client.incoming.get(timeout=1.0)
                    except queue.Empty:
                        if idle_exit is not None and time.monotonic() - last_activity >= idle_exit:
                            return "idle"
                        continue
                    if item is None:
                        raise MQTTDisconnected(client.disconnect_reason or "disconnected")
                    last_activity = time.monotonic()
                    try:
                        packet = json.loads(item.payload.decode("utf-8"))
                    except (UnicodeDecodeError, ValueError):
                        continue
                    if not isinstance(packet, dict) or "t" not in packet:
                        continue
                    try:
                        self.handle_packet(packet)
                    except AuthRequired:
                        raise
                    except (sqlite3.Error, HikeError, KeyError, TypeError, ValueError, AttributeError) as exc:
                        self.conn.rollback()
                        self.out.status(f"could not process a '{packet.get('t')}' packet: {exc}")
            except AuthRequired:
                raise
            except (MQTTError, HikeError) as exc:
                self.out.status(f"connection lost ({exc}); reconnecting in {backoff:.0f}s")
                self.out.emit("disconnected", reason=str(exc))
                stop.wait(backoff)
                backoff = min(backoff * 2, 60.0)
            finally:
                if client.connected:
                    try:
                        publish_packet(client, self.me, build_presence(self.me, False), qos=0)
                    except MQTTError:
                        pass
                client.disconnect()
                self.client = None
        return "stopped"
