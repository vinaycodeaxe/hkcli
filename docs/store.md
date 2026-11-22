---
title: Store
description: "What hikecli saves on disk, how to read the database from other tools, and how to delete it."
---

# Store

Everything hikecli saves lives in one folder per account: `~/.hikecli` by default
(`%USERPROFILE%\.hikecli` on Windows). Change it with `--store DIR` or `HIKECLI_STORE`.

| File | What it is |
| --- | --- |
| `session.json` | Your login (access and refresh tokens). Treat it like a password |
| `device.json` | The web device id hikecli uses with Hike |
| `hike.db` | Chats, messages, contacts and the search index (SQLite; the `-wal` and `-shm` files belong to it) |
| `media/` | Downloaded attachments |
| `LOCK`, `refresh.lock` | Lock files that stop two processes from clashing |
| `login-qr.png` | The QR image while you log in; deleted afterwards |

On macOS and Linux, the folder and the login files are readable only by you.

## Database

`hike.db` is a normal SQLite file:

| Table | Contents |
| --- | --- |
| `chats` | One row per chat: `id`, `type` (`one_to_one` or `group`), `name`, `phone`, unread count, mute, archive and pin flags, last message preview |
| `messages` | One row per message: `chat_id`, `server_id`, `client_id`, `sender_id`, `from_me`, `ts` (epoch ms), `kind`, `text`, file and reply fields, `state`, edit, delete and star flags, and `raw` (the original Hike packet) |
| `reactions` | Who reacted with which emoji to which message |
| `contacts` | Names, phone numbers and status messages |
| `group_members` | Members and roles of groups you looked up |
| `messages_fts` | The full-text search index, kept up to date automatically |

Other programs can read it, even while `sync` runs. Open it read-only:

```bash
sqlite3 "file:$HOME/.hikecli/hike.db?mode=ro" \
  "select datetime(ts/1000, 'unixepoch'), text from messages order by ts desc limit 10;"
```

```python
import sqlite3
from pathlib import Path

db = sqlite3.connect(f"file:{Path.home() / '.hikecli' / 'hike.db'}?mode=ro", uri=True)
for chat_id, count in db.execute("select chat_id, count(*) from messages group by chat_id"):
    print(chat_id, count)
```

Only write to it through hikecli. The table layout can change between versions; the
`--json` output of the commands stays stable.

## Privacy and deleting

The store is not encrypted. Anyone who can read the folder can read your messages, and
anyone with `session.json` can use your account. To remove everything:

```bash
hikecli auth logout
rm -rf ~/.hikecli
```
