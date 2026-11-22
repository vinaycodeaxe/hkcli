---
title: Messages
description: "List chats, read and search stored messages, and mark chats as read."
---

# Messages

These commands read the local store, so they work offline. Run [`sync`](sync.md) first to
bring it up to date.

## Chats

```bash
hkcli chats list                 # newest first, pinned chats on top
hkcli chats list --unread
hkcli chats list --groups        # or --direct
hkcli chats list --query asha    # name, phone or id
```

The `ID` column is what `--chat`, `--to` and `--group` accept when a name is ambiguous.

## Read a chat

```bash
hkcli messages list --chat Asha                  # latest 50, oldest first
hkcli messages list --chat Asha --limit 200
hkcli messages list --chat Asha --before 2026-11-01
```

`--before` takes `YYYY-MM-DD`, `"YYYY-MM-DD HH:MM"` or epoch milliseconds, which lets you
page backwards. Each line ends with the message id in parentheses.

## Search

```bash
hkcli messages search "lunch tomorrow"
hkcli messages search invoice --chat Ravi
hkcli messages search "deploy*" --limit 100
```

- Every word must appear in the message text, caption or file name.
- End a word with `*` to match prefixes: `deploy*` also finds "deployment".
- Matching ignores case and accents.
- Messages deleted for everyone are not searched.

Search uses SQLite FTS5, so it stays fast with years of messages.

## One message

```bash
hkcli messages show --chat Asha --id 23:1760000004000
hkcli messages show --chat Asha --id 23:1760000004000 --json   # includes the raw Hike packet
```

## Mark a chat as read

```bash
hkcli chats mark-read --chat Asha
```

This sends a read receipt for the latest message, so the sender sees it as read, and clears
the unread count.

## JSON

With `--json`, `chats list` returns chat objects (`id`, `type`, `name`, `phone`, `unread`,
`muted`, `archived`, `pinned`, `last_time`, `last_text` and more), and the `messages`
commands return message objects like this one:

```json
{
  "id": "23:1760000004000",
  "chat": "2002",
  "chat_name": "Asha",
  "time": "2025-10-09T08:53:24+00:00",
  "from_me": false,
  "sender": "2002",
  "sender_name": "Asha",
  "kind": "text",
  "text": "see you at noon",
  "state": "delivered",
  "reactions": {"1001": "👍"}
}
```

`kind` is one of `text`, `image`, `video`, `audio`, `document`, `sticker`, `gif`,
`location`, `contact`, `invite`, `call` or `system`. See [Commands](commands.md#json-output)
for the full format.
