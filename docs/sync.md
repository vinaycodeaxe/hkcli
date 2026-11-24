---
title: Sync
description: "How hkcli catches up over HTTP, then stays connected to save new messages, and how to run it as a service."
---

# Sync

```bash
hkcli sync          # catch up, then stay connected (Ctrl+C to stop)
hkcli sync --once   # catch up and exit
```

## How it works

1. **Catch-up.** hkcli downloads your chat list, then fetches the newest messages of chats
   that changed since the last run: up to `--max-chats` chats (default 50) and up to
   `--pages` pages (default 3) of `--batch` messages (default 50) per chat, stopping as soon
   as it reaches messages it already has. It also refreshes contacts and looks up names it
   does not know yet.
2. **Live.** Without `--once`, hkcli connects to Hike's message relay, the same one Hike
   Web uses, and saves everything that arrives: messages, delivery and read receipts,
   reactions, edits, deletes, chat changes (mute, archive, pin), calls and group changes.
3. **Reconnect.** If the connection drops, hkcli waits (1 second, doubling up to 60),
   reconnects and catches up again.

Older history is fetched on request with [`history backfill`](history.md).

## Options

| Flag | Meaning |
| --- | --- |
| `--once` | Catch up over HTTP and exit |
| `--idle-exit SECONDS` | Exit after this long without new events |
| `--max-reconnect SECONDS` | Give up after failing to connect for this long (default: keep trying) |
| `--max-chats N` | Chats to fetch messages for per catch-up (0 = all) |
| `--pages N` | Max history pages per chat per catch-up (default 3) |
| `--batch N` | Messages per history request (default 50) |
| `--presence quiet` | Default. Act like a background Hike Web tab |
| `--presence normal` | Act like an open Hike Web tab |
| `--no-receipts` | Do not send delivery receipts for messages fetched while offline |

When live, hkcli tells senders that their messages reached this device (delivery
receipts), as Hike Web does. It never sends read receipts on its own; use
[`chats mark-read`](messages.md#mark-a-chat-as-read) for that.

## Output

People see one line per new message on stdout; status goes to stderr. With `--json`, every
event is one JSON object per line on stdout:

| Event | When |
| --- | --- |
| `connected` | Connected to the relay |
| `sync` | Catch-up finished (`contacts`, `chats`, `chats_fetched`, `messages`, `receipts`) |
| `message` | New message (`chat`, `chat_name`, `chat_type` (`one_to_one` or `group`), `id`, `sender`, `sender_name`, `from_me`, `ts`, `kind`, `text`, `file_name`, `reply_to`, `mentions` (list of `uid`, `phone`, `start`, `length`), `mentions_me`) |
| `receipt` | Your messages were delivered or read (`chat`, `by`, `state`, `up_to`) |
| `reaction` | Someone reacted (`chat`, `target`, `user`, `emoji`; `emoji` is null when removed) |
| `edit`, `delete` | A message was edited, or deleted for everyone |
| `typing`, `presence` | Typing indicators and last-seen updates |
| `chat_action`, `message_action` | Mute, archive, pin, star and similar changes |
| `data_sync`, `packet` | Other notices from Hike |
| `status` | A progress or warning line |
| `disconnected` | The connection dropped (`reason`); hkcli reconnects |
| `stopped` | Sync ended (`reason`: `idle`, `interrupted` or `stopped`) |

```bash
hkcli sync --json | jq -r 'select(.event == "message") | "\(.chat_name): \(.text)"'
```

## One sync per store

Only one `sync` (or `auth`) can run per store at a time; a second one exits with
`store_locked`. Other commands, including `send`, keep working while sync runs.

## Sending while sync runs

While `hkcli sync` is connected, `send`, `chats mark-read` and `doctor --connect` on the same
store go out through its connection instead of opening a second one. Hike's relay can stop
delivering messages to a connection once another connection of the same account appears, so
messages would stay at one tick until sync reconnected. Sync records where to reach it in
`sync-control.json` in the store (a port on 127.0.0.1 and a random token) and deletes the file
when it stops. Without a running sync, commands connect on their own as before.

## Run sync as a service

To keep the store up to date all the time, run `hkcli sync` in the background. The examples
below use `/usr/local/bin/hkcli`; use the path that `command -v hkcli` prints on your machine
(for example `~/go/bin/hkcli` after `go install`).

**Linux (systemd user service).** Save as `~/.config/systemd/user/hkcli-sync.service`:

```ini
[Unit]
Description=hkcli sync
After=network-online.target

[Service]
ExecStart=/usr/local/bin/hkcli sync
Restart=on-failure
RestartSec=30
# Exit code 3 means the login needs attention, so do not restart in a loop.
RestartPreventExitStatus=3

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now hkcli-sync
loginctl enable-linger "$USER"     # keep it running after you log out
journalctl --user -u hkcli-sync -f
```

**macOS (launchd).** Save as `~/Library/LaunchAgents/dev.hkcli.sync.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.hkcli.sync</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/hkcli</string>
    <string>sync</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardErrorPath</key><string>/tmp/hkcli-sync.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.hkcli.sync.plist
```

If the service keeps stopping, run `hkcli auth status`: an expired login stops sync with
exit code 3.
