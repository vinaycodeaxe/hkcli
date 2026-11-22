---
title: Sync
description: "How hikecli catches up over HTTP, then stays connected to save new messages, and how to run it as a service."
---

# Sync

```bash
hikecli sync          # catch up, then stay connected (Ctrl+C to stop)
hikecli sync --once   # catch up and exit
```

## How it works

1. **Catch-up.** hikecli downloads your chat list, then fetches the newest messages of chats
   that changed since the last run: up to `--max-chats` chats (default 50) and up to
   `--pages` pages (default 3) of `--batch` messages (default 50) per chat, stopping as soon
   as it reaches messages it already has. It also refreshes contacts and looks up names it
   does not know yet.
2. **Live.** Without `--once`, hikecli connects to Hike's message relay, the same one Hike
   Web uses, and saves everything that arrives: messages, delivery and read receipts,
   reactions, edits, deletes, chat changes (mute, archive, pin), calls and group changes.
3. **Reconnect.** If the connection drops, hikecli waits (1 second, doubling up to 60),
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

When live, hikecli tells senders that their messages reached this device (delivery
receipts), as Hike Web does. It never sends read receipts on its own; use
[`chats mark-read`](messages.md#mark-a-chat-as-read) for that.

## Output

People see one line per new message on stdout; status goes to stderr. With `--json`, every
event is one JSON object per line on stdout:

| Event | When |
| --- | --- |
| `connected` | Connected to the relay |
| `sync` | Catch-up finished (`contacts`, `chats`, `chats_fetched`, `messages`, `receipts`) |
| `message` | New message (`chat`, `chat_name`, `id`, `sender`, `sender_name`, `from_me`, `ts`, `kind`, `text`, `file_name`, `reply_to`) |
| `receipt` | Your messages were delivered or read (`chat`, `by`, `state`, `up_to`) |
| `reaction` | Someone reacted (`chat`, `target`, `user`, `emoji`; `emoji` is null when removed) |
| `edit`, `delete` | A message was edited, or deleted for everyone |
| `typing`, `presence` | Typing indicators and last-seen updates |
| `chat_action`, `message_action` | Mute, archive, pin, star and similar changes |
| `data_sync`, `packet` | Other notices from Hike |
| `status` | A progress or warning line |
| `disconnected` | The connection dropped (`reason`); hikecli reconnects |
| `stopped` | Sync ended (`reason`: `idle`, `interrupted` or `stopped`) |

```bash
hikecli sync --json | jq -r 'select(.event == "message") | "\(.chat_name): \(.text)"'
```

## One sync per store

Only one `sync` (or `auth`) can run per store at a time; a second one exits with
`store_locked`. Other commands, including `send`, keep working while sync runs.

## Run sync as a service

To keep the store up to date all the time, run `hikecli sync` in the background.

**Linux (systemd user service).** Save as `~/.config/systemd/user/hikecli-sync.service`:

```ini
[Unit]
Description=hikecli sync
After=network-online.target

[Service]
ExecStart=%h/.local/bin/hikecli sync
Restart=on-failure
RestartSec=30
# Exit code 3 means the login needs attention, so do not restart in a loop.
RestartPreventExitStatus=3

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now hikecli-sync
loginctl enable-linger "$USER"     # keep it running after you log out
journalctl --user -u hikecli-sync -f
```

**macOS (launchd).** Save as `~/Library/LaunchAgents/dev.hikecli.sync.plist`, replacing
`YOU` with your user name:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.hikecli.sync</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/.local/bin/hikecli</string>
    <string>sync</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardErrorPath</key><string>/tmp/hikecli-sync.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.hikecli.sync.plist
```

If the service keeps stopping, run `hikecli auth status`: an expired login stops sync with
exit code 3.
