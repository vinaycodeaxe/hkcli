---
title: History
description: "Fetch older messages for a chat from Hike's server."
---

# History

`sync` fetches the newest messages of each chat. Older messages stay on Hike's server until
you ask for them:

```bash
hkcli history backfill --chat Asha              # 5 pages of 50 messages
hkcli history backfill --chat Asha --pages 20
```

Each run continues where the previous one stopped. When Hike has no older messages, hkcli
says it reached the start of the chat, and later runs return right away.

| Flag | Meaning |
| --- | --- |
| `--chat` | Chat name, phone number or id (required) |
| `--pages N` | Pages to fetch this run (default 5) |
| `--batch N` | Messages per page (default 50) |

To backfill every chat:

```bash
hkcli --json chats list --limit 1000 | jq -r '.data[].id' |
  while read -r id; do hkcli history backfill --chat "$id" --pages 20; done
```

History is best effort: Hike's server decides how far back it goes. Unlike WhatsApp, your
phone does not need to be online for this, because the history comes from Hike's server.
