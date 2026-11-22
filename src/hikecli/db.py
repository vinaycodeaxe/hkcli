"""Local SQLite mirror with FTS5 search."""

from __future__ import annotations

import sqlite3
from pathlib import Path

from .protocol import STATE_RANK, Message, server_seq, summary

SCHEMA_VERSION = 1

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT
);
CREATE TABLE IF NOT EXISTS chats (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL DEFAULT 'one_to_one',
    name TEXT,
    phone TEXT,
    unread INTEGER NOT NULL DEFAULT 0,
    api_unread INTEGER NOT NULL DEFAULT 0,
    undelivered INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    muted INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    blocked INTEGER NOT NULL DEFAULT 0,
    last_ts INTEGER,
    last_text TEXT,
    last_sender TEXT,
    last_server_id TEXT,
    history_cursor TEXT,
    history_done INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY,
    chat_id TEXT NOT NULL,
    server_id TEXT,
    client_id TEXT,
    sender_id TEXT NOT NULL,
    from_me INTEGER NOT NULL DEFAULT 0,
    ts INTEGER NOT NULL,
    kind TEXT NOT NULL,
    subtype TEXT,
    text TEXT,
    file_id TEXT,
    file_name TEXT,
    mime TEXT,
    file_size INTEGER,
    duration INTEGER,
    width INTEGER,
    height INTEGER,
    latitude REAL,
    longitude REAL,
    address TEXT,
    contact_name TEXT,
    contact_phone TEXT,
    contact_user_id TEXT,
    reply_to_id TEXT,
    reply_to_sender TEXT,
    reply_to_text TEXT,
    forwarded INTEGER NOT NULL DEFAULT 0,
    sender_name TEXT,
    state TEXT,
    edited INTEGER NOT NULL DEFAULT 0,
    deleted_for_me INTEGER NOT NULL DEFAULT 0,
    deleted_for_all INTEGER NOT NULL DEFAULT 0,
    starred INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    local_path TEXT,
    raw TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS messages_server_id ON messages(chat_id, server_id);
CREATE UNIQUE INDEX IF NOT EXISTS messages_client_id ON messages(chat_id, client_id);
CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages(chat_id, ts);
CREATE TABLE IF NOT EXISTS reactions (
    chat_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    emoji TEXT NOT NULL,
    ts INTEGER,
    PRIMARY KEY (chat_id, target_id, sender_id)
);
CREATE TABLE IF NOT EXISTS contacts (
    user_id TEXT PRIMARY KEY,
    name TEXT,
    phone TEXT,
    status TEXT,
    pic TEXT,
    in_address_book INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER
);
CREATE INDEX IF NOT EXISTS contacts_phone ON contacts(phone);
CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT,
    name TEXT,
    phone TEXT,
    PRIMARY KEY (group_id, user_id)
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text, file_name, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text, file_name) VALUES (new.id, new.text, new.file_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text, file_name) VALUES ('delete', old.id, old.text, old.file_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE OF text, file_name ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text, file_name) VALUES ('delete', old.id, old.text, old.file_name);
    INSERT INTO messages_fts(rowid, text, file_name) VALUES (new.id, new.text, new.file_name);
END;
"""

MESSAGE_COLUMNS = (
    "chat_id", "server_id", "client_id", "sender_id", "from_me", "ts", "kind", "subtype", "text",
    "file_id", "file_name", "mime", "file_size", "duration", "width", "height", "latitude", "longitude",
    "address", "contact_name", "contact_phone", "contact_user_id", "reply_to_id", "reply_to_sender",
    "reply_to_text", "forwarded", "sender_name", "state", "edited", "deleted_for_me", "deleted_for_all",
    "starred", "pinned", "raw",
)
FLAG_COLUMNS = ("edited", "deleted_for_me", "deleted_for_all", "starred", "pinned")
CHAT_COLUMNS = (
    "type", "name", "phone", "unread", "api_unread", "undelivered", "archived", "muted", "pinned", "blocked",
    "last_ts", "last_text", "last_sender", "last_server_id", "history_cursor", "history_done",
)


def connect(path: str | Path) -> sqlite3.Connection:
    conn = sqlite3.connect(str(path), timeout=30)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=30000")
    conn.execute("PRAGMA synchronous=NORMAL")
    conn.executescript(SCHEMA)
    conn.execute(f"PRAGMA user_version={SCHEMA_VERSION}")
    conn.commit()
    return conn


def fts5_available() -> bool:
    try:
        probe = sqlite3.connect(":memory:")
        probe.execute("CREATE VIRTUAL TABLE t USING fts5(x)")
        probe.close()
        return True
    except sqlite3.Error:
        return False


# ---- meta -------------------------------------------------------------------


def get_meta(conn: sqlite3.Connection, key: str) -> str | None:
    row = conn.execute("SELECT value FROM meta WHERE key=?", (key,)).fetchone()
    return row["value"] if row else None


def set_meta(conn: sqlite3.Connection, key: str, value) -> None:
    conn.execute(
        "INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (key, None if value is None else str(value)),
    )


# ---- chats -----------------------------------------------------------------


def ensure_chat(conn: sqlite3.Connection, chat_id: str, chat_type: str | None = None) -> None:
    conn.execute(
        "INSERT OR IGNORE INTO chats(id, type, updated_at) VALUES(?, ?, strftime('%s','now'))",
        (chat_id, chat_type or "one_to_one"),
    )


def update_chat(conn: sqlite3.Connection, chat_id: str, **fields) -> None:
    unknown = set(fields) - set(CHAT_COLUMNS)
    if unknown:
        raise ValueError(f"unknown chat fields: {sorted(unknown)}")
    ensure_chat(conn, chat_id, fields.get("type"))
    if not fields:
        return
    assignments = ", ".join(f"{name}=?" for name in fields)
    conn.execute(
        f"UPDATE chats SET {assignments}, updated_at=strftime('%s','now') WHERE id=?",
        (*fields.values(), chat_id),
    )


def get_chat(conn: sqlite3.Connection, chat_id: str) -> sqlite3.Row | None:
    return conn.execute("SELECT * FROM chats WHERE id=?", (chat_id,)).fetchone()


def touch_chat_last(conn: sqlite3.Connection, msg: Message) -> None:
    """Move the chat's last-message preview forward if this message is newer."""
    ensure_chat(conn, msg.chat_id)
    preview = summary(msg.kind, msg.text, msg.file_name, bool(msg.deleted_for_all))
    conn.execute(
        """UPDATE chats SET last_ts=?, last_text=?, last_sender=?, last_server_id=COALESCE(?, last_server_id)
           WHERE id=? AND (last_ts IS NULL OR last_ts <= ?)""",
        (msg.ts, preview, msg.sender_id, msg.server_id, msg.chat_id, msg.ts),
    )


def list_chats(conn: sqlite3.Connection, *, unread_only: bool = False, query: str | None = None, chat_type: str | None = None, limit: int = 50):
    where, params = [], []
    if unread_only:
        where.append("unread > 0")
    if chat_type:
        where.append("type = ?")
        params.append(chat_type)
    if query:
        where.append("(name LIKE ? OR phone LIKE ? OR id = ?)")
        params += [f"%{query}%", f"%{query}%", query]
    sql = "SELECT * FROM chats"
    if where:
        sql += " WHERE " + " AND ".join(where)
    sql += " ORDER BY pinned DESC, COALESCE(last_ts, 0) DESC LIMIT ?"
    return conn.execute(sql, (*params, limit)).fetchall()


def chat_last_local_ts(conn: sqlite3.Connection, chat_id: str) -> int | None:
    row = conn.execute("SELECT MAX(ts) AS ts FROM messages WHERE chat_id=?", (chat_id,)).fetchone()
    return row["ts"] if row else None


# ---- messages --------------------------------------------------------------


def find_message(conn: sqlite3.Connection, chat_id: str, message_id: str) -> sqlite3.Row | None:
    return conn.execute(
        "SELECT * FROM messages WHERE chat_id=? AND (server_id=? OR client_id=?) ORDER BY id LIMIT 1",
        (chat_id, message_id, message_id),
    ).fetchone()


def _existing(conn: sqlite3.Connection, msg: Message) -> sqlite3.Row | None:
    row = None
    if msg.server_id:
        row = conn.execute("SELECT * FROM messages WHERE chat_id=? AND server_id=?", (msg.chat_id, msg.server_id)).fetchone()
    if row is None and msg.client_id:
        row = conn.execute("SELECT * FROM messages WHERE chat_id=? AND client_id=?", (msg.chat_id, msg.client_id)).fetchone()
    return row


def upsert_message(conn: sqlite3.Connection, msg: Message) -> tuple[int, bool]:
    """Insert or merge by server id or client id. Returns (row id, inserted)."""
    values = {name: getattr(msg, name) for name in MESSAGE_COLUMNS}
    values["from_me"] = int(msg.from_me)
    values["forwarded"] = int(msg.forwarded)
    row = _existing(conn, msg)
    if row is None:
        for flag in FLAG_COLUMNS:
            values[flag] = int(bool(values[flag]))
        cols = ", ".join(MESSAGE_COLUMNS)
        marks = ", ".join("?" for _ in MESSAGE_COLUMNS)
        cur = conn.execute(f"INSERT INTO messages({cols}) VALUES({marks})", [values[c] for c in MESSAGE_COLUMNS])
        rowid, inserted = cur.lastrowid, True
    else:
        merged = {}
        for name in MESSAGE_COLUMNS:
            new, old = values[name], row[name]
            if name in FLAG_COLUMNS:
                merged[name] = old if new is None else int(bool(new))
            elif name == "state":
                merged[name] = new if STATE_RANK.get(new or "", -1) > STATE_RANK.get(old or "", -1) else old
            elif name in ("server_id", "client_id"):
                merged[name] = old or new
            elif name in ("from_me", "forwarded"):
                merged[name] = max(int(old or 0), int(new or 0))
            else:
                merged[name] = old if new is None else new
        if merged["deleted_for_all"]:
            merged["text"] = None
        assignments = ", ".join(f"{name}=?" for name in MESSAGE_COLUMNS)
        conn.execute(f"UPDATE messages SET {assignments} WHERE id=?", [merged[c] for c in MESSAGE_COLUMNS] + [row["id"]])
        rowid, inserted = row["id"], False
    if msg.reactions:
        target = msg.server_id or msg.client_id
        conn.execute("DELETE FROM reactions WHERE chat_id=? AND target_id=?", (msg.chat_id, target))
        conn.executemany(
            "INSERT INTO reactions(chat_id, target_id, sender_id, emoji, ts) VALUES(?, ?, ?, ?, ?)",
            [(msg.chat_id, target, sender, emoji, msg.ts) for sender, emoji in msg.reactions.items()],
        )
    return rowid, inserted


def list_messages(conn: sqlite3.Connection, chat_id: str, *, limit: int = 50, before: int | None = None):
    """Latest `limit` messages (older than `before` if given), returned oldest first."""
    sql = "SELECT * FROM messages WHERE chat_id=? AND deleted_for_me=0"
    params: list = [chat_id]
    if before is not None:
        sql += " AND ts < ?"
        params.append(before)
    sql += " ORDER BY ts DESC, id DESC LIMIT ?"
    params.append(limit)
    return list(reversed(conn.execute(sql, params).fetchall()))


def to_fts_query(query: str) -> str:
    terms = []
    for token in query.split():
        prefix = token.endswith("*") and len(token) > 1
        word = token[:-1] if prefix else token
        word = word.replace('"', '""')
        if word:
            terms.append(f'"{word}"*' if prefix else f'"{word}"')
    if not terms:
        raise ValueError("search query is empty")
    return " ".join(terms)


def search_messages(conn: sqlite3.Connection, query: str, *, chat_id: str | None = None, limit: int = 50):
    sql = """SELECT m.* FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
             WHERE messages_fts MATCH ? AND m.deleted_for_me=0 AND m.deleted_for_all=0"""
    params: list = [to_fts_query(query)]
    if chat_id:
        sql += " AND m.chat_id=?"
        params.append(chat_id)
    sql += " ORDER BY m.ts DESC LIMIT ?"
    params.append(limit)
    return conn.execute(sql, params).fetchall()


def reactions_for(conn: sqlite3.Connection, chat_id: str, target_id: str) -> dict:
    rows = conn.execute("SELECT sender_id, emoji FROM reactions WHERE chat_id=? AND target_id=?", (chat_id, target_id)).fetchall()
    return {row["sender_id"]: row["emoji"] for row in rows}


def set_reaction(conn: sqlite3.Connection, chat_id: str, target_id: str, sender_id: str, emoji: str | None, ts: int) -> None:
    if emoji:
        conn.execute(
            """INSERT INTO reactions(chat_id, target_id, sender_id, emoji, ts) VALUES(?, ?, ?, ?, ?)
               ON CONFLICT(chat_id, target_id, sender_id) DO UPDATE SET emoji=excluded.emoji, ts=excluded.ts""",
            (chat_id, target_id, sender_id, emoji, ts),
        )
    else:
        conn.execute("DELETE FROM reactions WHERE chat_id=? AND target_id=? AND sender_id=?", (chat_id, target_id, sender_id))


def mark_my_messages(conn: sqlite3.Connection, chat_id: str, up_to_seq: int, state: str) -> int:
    """Raise the state of my messages with seq <= up_to_seq (Hike receipts are cumulative)."""
    rank = STATE_RANK[state]
    rows = conn.execute(
        "SELECT id, server_id, state FROM messages WHERE chat_id=? AND from_me=1 AND server_id IS NOT NULL",
        (chat_id,),
    ).fetchall()
    ids = [
        row["id"]
        for row in rows
        if (server_seq(row["server_id"]) or 0) <= up_to_seq and STATE_RANK.get(row["state"] or "", -1) < rank
    ]
    conn.executemany("UPDATE messages SET state=? WHERE id=?", [(state, i) for i in ids])
    return len(ids)


def apply_message_action(conn: sqlite3.Connection, chat_id: str, action: str, target_id: str, *, text=None, value=None, by_me=False) -> int:
    where = "chat_id=? AND (server_id=? OR client_id=?)"
    args = (chat_id, target_id, target_id)
    if action == "em":
        cur = conn.execute(f"UPDATE messages SET text=?, edited=1 WHERE {where} AND deleted_for_all=0", (text, *args))
    elif action == "dma":
        cur = conn.execute(f"UPDATE messages SET text=NULL, deleted_for_all=1 WHERE {where}", args)
    elif action == "dm":
        if not by_me:
            return 0
        cur = conn.execute(f"UPDATE messages SET deleted_for_me=1 WHERE {where}", args)
    elif action == "sm":
        cur = conn.execute(f"UPDATE messages SET starred=? WHERE {where}", (int(bool(value)), *args))
    elif action == "pm":
        cur = conn.execute(f"UPDATE messages SET pinned=? WHERE {where}", (int(bool(value)), *args))
    else:
        return 0
    return cur.rowcount


# ---- contacts and groups -------------------------------------------------------


def upsert_contact(conn: sqlite3.Connection, user_id: str, *, name=None, phone=None, status=None, pic=None, in_address_book=None, overwrite_name=True) -> None:
    name_sql = "COALESCE(excluded.name, contacts.name)" if overwrite_name else "COALESCE(contacts.name, excluded.name)"
    conn.execute(
        f"""INSERT INTO contacts(user_id, name, phone, status, pic, in_address_book, updated_at)
            VALUES(?, ?, ?, ?, ?, ?, strftime('%s','now'))
            ON CONFLICT(user_id) DO UPDATE SET
              name={name_sql},
              phone=COALESCE(excluded.phone, contacts.phone),
              status=COALESCE(excluded.status, contacts.status),
              pic=COALESCE(excluded.pic, contacts.pic),
              in_address_book=CASE WHEN ? IS NULL THEN contacts.in_address_book ELSE excluded.in_address_book END,
              updated_at=excluded.updated_at""",
        (user_id, name, phone, status, pic, int(bool(in_address_book)), in_address_book),
    )


def list_contacts(conn: sqlite3.Connection, *, query: str | None = None, limit: int = 200):
    sql = "SELECT * FROM contacts"
    params: list = []
    if query:
        sql += " WHERE name LIKE ? OR phone LIKE ? OR user_id = ?"
        params += [f"%{query}%", f"%{query}%", query]
    sql += " ORDER BY in_address_book DESC, COALESCE(name, phone, user_id) COLLATE NOCASE LIMIT ?"
    return conn.execute(sql, (*params, limit)).fetchall()


def display_name(conn: sqlite3.Connection, user_id: str | None, me: str | None = None) -> str:
    if not user_id:
        return ""
    if me and user_id == me:
        return "me"
    row = conn.execute("SELECT name, phone FROM contacts WHERE user_id=?", (user_id,)).fetchone()
    if row and (row["name"] or row["phone"]):
        return row["name"] or row["phone"]
    chat = conn.execute("SELECT name FROM chats WHERE id=? AND type='one_to_one'", (user_id,)).fetchone()
    return chat["name"] if chat and chat["name"] else user_id


def unnamed_senders(conn: sqlite3.Connection, me: str, limit: int = 200) -> list[str]:
    rows = conn.execute(
        """SELECT DISTINCT m.sender_id FROM messages m
           LEFT JOIN contacts c ON c.user_id = m.sender_id
           WHERE m.from_me=0 AND m.sender_id != '' AND m.sender_id != ? AND (c.user_id IS NULL OR c.name IS NULL)
           LIMIT ?""",
        (me, limit),
    ).fetchall()
    return [row["sender_id"] for row in rows]


def replace_group_members(conn: sqlite3.Connection, group_id: str, members: list[dict]) -> None:
    conn.execute("DELETE FROM group_members WHERE group_id=?", (group_id,))
    conn.executemany(
        "INSERT OR REPLACE INTO group_members(group_id, user_id, role, name, phone) VALUES(?, ?, ?, ?, ?)",
        [(group_id, str(m.get("userId")), m.get("role"), m.get("name"), m.get("phoneNumber")) for m in members if m.get("userId")],
    )


def group_members(conn: sqlite3.Connection, group_id: str):
    return conn.execute(
        "SELECT * FROM group_members WHERE group_id=? ORDER BY CASE role WHEN 'OWNER' THEN 0 WHEN 'ADMIN' THEN 1 ELSE 2 END, name",
        (group_id,),
    ).fetchall()


def stats(conn: sqlite3.Connection) -> dict:
    one = lambda sql: conn.execute(sql).fetchone()[0]  # noqa: E731
    return {
        "chats": one("SELECT COUNT(*) FROM chats"),
        "messages": one("SELECT COUNT(*) FROM messages"),
        "contacts": one("SELECT COUNT(*) FROM contacts"),
        "oldest_ts": one("SELECT MIN(ts) FROM messages"),
        "newest_ts": one("SELECT MAX(ts) FROM messages"),
    }
