"""Hike Web message packets.

The same JSON packet format is used live over MQTT and inside msg-history REST
items (wrapped as {"messageJson": "<packet>", "state": ..., ...}). Field names
are Hike's short keys: t=type, st=subtype, f=from, to=to, c=conversation,
i=client id, s=server id ("seq:timestamp"), ts=epoch ms, d=data.
"""

from __future__ import annotations

import json
import re
import time
import uuid
from dataclasses import dataclass, field

# Packet types ("t")
T_MESSAGE = "m"
T_DELIVERY = "dr"
T_FIRST_DELIVERY = "fdr"
T_MARK_READ = "mr"
T_FIRST_MARK_READ = "fmr"
T_GROUP_ACTION = "gca"
T_SYSTEM = "s"
T_CALL = "v"
T_CONVERSATION_ACTION = "ca"
T_MESSAGE_ACTION = "ma"
T_REACTION = "r"
T_INVITE = "inv"
T_START_TYPING = "st"
T_END_TYPING = "et"
T_FOREGROUND = "fg"
T_BACKGROUND = "bg"
T_PRESENCE = "pr"
T_DATA_SYNC = "ds"

# Message subtypes ("st")
ST_TEXT = "text"
ST_FILE = "file"
ST_ADD_REACTION = "ar"
ST_REMOVE_REACTION = "rr"
ST_GROUP_INVITE = "gi"
ST_INVITE_STATUS_UPDATE = "giu"

KIND_BY_SUBTYPE = {
    "text": "text",
    "img": "image",
    "vimg": "image",
    "vid": "video",
    "vvid": "video",
    "audio": "audio",
    "vaud": "audio",
    "file": "document",
    "stk": "sticker",
    "gif": "gif",
    "loc": "location",
    "cn": "contact",
    "gi": "invite",
    "sbm": "system",
    "tsm": "system",
}

# Message actions ("ma" d.st)
MA_EDIT = "em"
MA_DELETE_FOR_ME = "dm"
MA_DELETE_FOR_ALL = "dma"
MA_STAR = "sm"
MA_PIN = "pm"

# Conversation actions ("ca" d.st)
CA_CLEAR = "cc"
CA_ARCHIVE = "ac"
CA_DELETE = "dc"
CA_MUTE = "mc"
CA_BLOCK = "bc"
CA_UNREAD = "uc"
CA_PIN = "pc"

GROUP_ACTIONS = {"gcj": "joined", "gcl": "left", "gcr": "removed", "gcac": "admin change", "gu": "group updated"}
CALL_SUBTYPES = {
    "cinit": "started",
    "cinv": "ringing",
    "ciack": "ringing",
    "cdec": "declined",
    "ccan": "cancelled",
    "cip": "in progress",
    "ce": "ended",
    "csc": "updated",
    "cbsy": "busy",
}

STATE_FROM_API = {"READ": "read", "DELIVERED": "delivered", "SENT": "sent", "PENDING": "pending"}
STATE_RANK = {"failed": 0, "pending": 0, "sent": 1, "delivered": 2, "read": 3, "opened": 4}

_SERVER_ID = re.compile(r"^(\d+):(\d+)$")


def now_ms() -> int:
    return int(time.time() * 1000)


def new_client_id() -> str:
    return str(uuid.uuid4())


def as_str(value) -> str | None:
    if value is None or value == "":
        return None
    if isinstance(value, float) and value.is_integer():
        value = int(value)
    return str(value)


def as_int(value) -> int | None:
    try:
        return int(float(value))
    except (TypeError, ValueError):
        return None


def as_float(value) -> float | None:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def server_seq(server_id: str | None) -> int | None:
    match = _SERVER_ID.match(server_id or "")
    return int(match.group(1)) if match else None


def _strip_user_prefix(value: str | None) -> str | None:
    return value[2:] if value and value.startswith("u:") else value


def conversation_id(packet: dict, me: str) -> str | None:
    """Hike Web's rule: incoming 1:1 -> sender, everything else -> `to` (peer or group)."""
    sender = as_str(packet.get("f"))
    target = _strip_user_prefix(as_str(packet.get("to")))
    if sender == me and target == me:
        return None
    if target == me:
        return sender
    return target


# ---- outgoing packets -------------------------------------------------------


def _reply_block(reply: dict | None) -> dict:
    if not reply:
        return {}
    return {
        "rd": {
            "s": reply.get("id"),
            "f": reply.get("sender_id"),
            "st": reply.get("subtype"),
            "pt": reply.get("text") or "",
            "tn": None,
        }
    }


def build_text(me: str, conv: str, text: str, *, client_id: str | None = None, ts: int | None = None, reply: dict | None = None) -> dict:
    cid = client_id or new_client_id()
    ts = ts or now_ms()
    data = {"st": ST_TEXT, "ts": ts, "i": cid, "m": text, "p": text}
    return {"t": T_MESSAGE, "st": ST_TEXT, "c": conv, "f": me, "i": cid, "ts": ts, "to": conv, "d": data, **_reply_block(reply)}


def build_file(
    me: str,
    conv: str,
    *,
    file_id: str,
    file_size: int,
    mime: str,
    file_name: str,
    caption: str | None = None,
    client_id: str | None = None,
    ts: int | None = None,
    reply: dict | None = None,
) -> dict:
    cid = client_id or new_client_id()
    ts = ts or now_ms()
    data = {"st": ST_FILE, "ts": ts, "i": cid, "fid": file_id, "p": file_name, "s": file_size, "mime": mime, "fn": file_name}
    if caption:
        data["m"] = caption
    packet = {"t": T_MESSAGE, "st": ST_FILE, "f": me, "i": cid, "ts": ts, "c": conv, "seq": ts, "node": "default", "to": conv, "d": data}
    return {**packet, **_reply_block(reply)}


def build_mark_read(me: str, conv: str, sender_id: str, server_id: str) -> dict:
    return {"t": T_MARK_READ, "f": me, "to": conv, "i": new_client_id(), "d": {sender_id: server_id}, "ts": now_ms()}


def build_first_mark_read(me: str, conv: str, server_id: str) -> dict:
    return {"t": T_FIRST_MARK_READ, "f": me, "to": conv, "d": {"s": server_id}, "ts": now_ms()}


def build_first_delivery(me: str, conv: str, server_id: str) -> dict:
    return {"t": T_FIRST_DELIVERY, "f": me, "to": conv, "d": {"s": server_id}, "ts": now_ms()}


def build_presence(me: str, foreground: bool) -> dict:
    return {"t": T_FOREGROUND if foreground else T_BACKGROUND, "f": me, "i": new_client_id(), "ts": now_ms()}


# ---- incoming packets -------------------------------------------------------


@dataclass
class Message:
    chat_id: str
    sender_id: str
    from_me: bool
    ts: int
    kind: str
    subtype: str | None = None
    server_id: str | None = None
    client_id: str | None = None
    text: str | None = None
    file_id: str | None = None
    file_name: str | None = None
    mime: str | None = None
    file_size: int | None = None
    duration: int | None = None
    width: int | None = None
    height: int | None = None
    latitude: float | None = None
    longitude: float | None = None
    address: str | None = None
    contact_name: str | None = None
    contact_phone: str | None = None
    contact_user_id: str | None = None
    reply_to_id: str | None = None
    reply_to_sender: str | None = None
    reply_to_text: str | None = None
    forwarded: bool = False
    sender_name: str | None = None
    sender_phone: str | None = None
    state: str | None = None
    # None means "unknown" so a live packet never clears flags learned from history.
    edited: bool | None = None
    deleted_for_me: bool | None = None
    deleted_for_all: bool | None = None
    starred: bool | None = None
    pinned: bool | None = None
    reactions: dict = field(default_factory=dict)
    raw: str = ""

    @property
    def id(self) -> str | None:
        return self.server_id or self.client_id


def packet_from_history_item(item: dict) -> dict | None:
    if not isinstance(item, dict):
        return None
    raw = item.get("messageJson")
    if isinstance(raw, str):
        try:
            packet = json.loads(raw)
        except ValueError:
            return None
        return packet if isinstance(packet, dict) else None
    return item


def _call_text(data: dict) -> str:
    stage = CALL_SUBTYPES.get(data.get("st") or "", data.get("st") or "call")
    media = data.get("mt") or data.get("ct")
    parts = [f"{media} call" if media else "call", stage]
    seconds = as_int(data.get("dur"))
    if seconds:
        parts.append(f"{seconds // 60}m {seconds % 60}s")
    return " ".join(parts)


def _group_action_text(packet: dict, data: dict) -> str:
    sub = packet.get("st") or data.get("st") or ""
    label = GROUP_ACTIONS.get(sub, sub or "group update")
    names = [m.get("n") for m in (data.get("nm") or data.get("m") or []) if isinstance(m, dict) and m.get("n")]
    if data.get("gn") and sub == "gu":
        return f"group renamed to {data['gn']}"
    return f"{label}: {', '.join(names)}" if names else label


def parse_message(packet: dict, me: str, meta: dict | None = None, chat_id: str | None = None) -> Message | None:
    """Turn a message-like packet (m, inv, v, gca) into a Message; None for anything else."""
    if not isinstance(packet, dict):
        return None
    ptype = packet.get("t")
    if ptype not in (T_MESSAGE, T_INVITE, T_CALL, T_GROUP_ACTION):
        return None
    data = packet.get("d") if isinstance(packet.get("d"), dict) else {}
    sender = as_str(packet.get("f")) or ""
    if ptype == T_GROUP_ACTION:
        chat = chat_id or as_str(packet.get("c")) or as_str(data.get("gid")) or _strip_user_prefix(as_str(packet.get("to"))) or sender
    else:
        chat = chat_id or conversation_id(packet, me)
    if not chat:
        return None
    ts = as_int(packet.get("ts")) or as_int(data.get("ts")) or 0
    server_id = as_str(packet.get("s")) or as_str(packet.get("sid"))
    client_id = as_str(packet.get("i"))
    subtype = as_str(packet.get("st")) or as_str(data.get("st"))
    meta = meta or {}

    msg = Message(chat_id=chat, sender_id=sender, from_me=sender == me, ts=ts, kind="text", subtype=subtype, server_id=server_id, client_id=client_id)
    profile = packet.get("pi") if isinstance(packet.get("pi"), dict) else {}
    msg.sender_name = as_str(profile.get("n"))
    msg.sender_phone = as_str(profile.get("p"))

    if ptype == T_CALL:
        msg.kind = "call"
        room = as_str(data.get("rid"))
        if room:
            msg.client_id = f"call-{room}"
        msg.text = _call_text(data)
        msg.duration = as_int(data.get("dur"))
    elif ptype == T_GROUP_ACTION:
        msg.kind = "system"
        msg.text = _group_action_text(packet, data)
        if not msg.server_id and not msg.client_id:
            msg.client_id = f"gca-{ts}-{subtype or ''}"
    elif ptype == T_INVITE:
        if subtype not in (None, ST_GROUP_INVITE):
            return None
        msg.kind = "invite"
        msg.text = f"group invite: {data.get('gn') or ''}".strip()
    else:
        msg.kind = KIND_BY_SUBTYPE.get(subtype or "text", "text")
        text = data.get("m")
        msg.text = text if isinstance(text, str) and text != "" else None
        if msg.kind in ("image", "video", "audio", "document", "gif", "contact"):
            msg.file_id = as_str(data.get("fid")) or (as_str(data.get("p")) if msg.kind in ("image", "video", "gif") else None)
        msg.file_name = as_str(data.get("fn"))
        msg.mime = as_str(data.get("mime"))
        msg.file_size = as_int(data.get("s"))
        msg.duration = as_int(data.get("dur"))
        msg.width = as_int(data.get("w"))
        msg.height = as_int(data.get("h"))
        if msg.kind == "location":
            msg.latitude = as_float(data.get("lat"))
            msg.longitude = as_float(data.get("lng"))
            msg.address = as_str(data.get("add"))
            msg.text = msg.text or msg.address
        elif msg.kind == "contact":
            msg.contact_name = as_str(data.get("un")) or msg.text
            msg.contact_phone = " ".join(p for p in (as_str(data.get("cod")), as_str(data.get("cn"))) if p) or None
            msg.contact_user_id = as_str(data.get("cid"))
        elif msg.kind == "invite":
            msg.text = f"group invite: {data.get('gn') or ''}".strip()

    reply = packet.get("rd") if isinstance(packet.get("rd"), dict) else None
    if reply:
        msg.reply_to_id = as_str(reply.get("s"))
        msg.reply_to_sender = as_str(reply.get("f"))
        msg.reply_to_text = as_str(reply.get("pt"))
    msg.forwarded = isinstance(packet.get("fwd"), dict)

    api_state = meta.get("state")
    if isinstance(api_state, str) and api_state in STATE_FROM_API:
        msg.state = STATE_FROM_API[api_state]
    elif msg.from_me:
        msg.state = "sent" if msg.server_id else "pending"
    else:
        msg.state = "delivered"
    for attr, key in (("edited", "edited"), ("deleted_for_me", "deletedForMe"), ("deleted_for_all", "deletedForAll"), ("starred", "starred"), ("pinned", "pinned")):
        if key in meta and meta[key] is not None:
            setattr(msg, attr, bool(meta[key]))
    if msg.deleted_for_all:
        msg.text = None
    reactions = meta.get("reactionMap")
    if isinstance(reactions, dict):
        msg.reactions = {str(k): str(v) for k, v in reactions.items() if v}
    msg.raw = json.dumps(packet, ensure_ascii=False, separators=(",", ":"))
    return msg


KIND_LABELS = {
    "image": "[image]",
    "video": "[video]",
    "audio": "[audio]",
    "document": "[file]",
    "sticker": "[sticker]",
    "gif": "[gif]",
    "location": "[location]",
    "contact": "[contact]",
    "invite": "[invite]",
    "call": "[call]",
    "system": "[info]",
}


def summary(kind: str, text: str | None, file_name: str | None = None, deleted: bool = False) -> str:
    """One-line preview, like Hike Web's chat list."""
    if deleted:
        return "This message was deleted"
    label = KIND_LABELS.get(kind)
    if label is None:
        return text or ""
    bits = [label]
    if file_name and kind in ("document", "audio"):
        bits.append(file_name)
    if text:
        bits.append(text)
    return " ".join(bits)
