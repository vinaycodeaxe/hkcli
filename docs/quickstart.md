---
title: Quickstart
description: "Log in, sync, search and send your first Hike message from the terminal in five minutes."
---

# Quickstart

## 1. Install

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest
hkcli --version
```

No Go? Download the file for your system from the
[latest release](https://github.com/vinaycodeaxe/hkcli/releases/latest); see [Install](install.md).

## 2. Link your Hike account

```bash
hkcli auth
```

`auth` prints a QR code. On your phone, open Hike, go to **Settings > Web QR Login**, and
scan it. Once the phone confirms, hkcli saves the login and downloads your chats and
recent messages.

If the code does not scan, run `hkcli auth --qr-format png` to open it as an image, or
log in with an SMS code: `hkcli auth --phone +919876543210`. More on [Auth](auth.md).

## 3. Look around

```bash
hkcli chats list
hkcli messages list --chat Asha
hkcli messages search "lunch"
```

`--chat` takes a name, a phone number with country code, or the id shown by `chats list`.

## 4. Send a message

```bash
hkcli send text --to Asha --message "hi from the terminal"
hkcli send file --to Asha --file ./notes.pdf --caption "notes"
```

See [Send](send.md) for replies, piped text and how recipients are matched.

## 5. Keep it up to date

```bash
hkcli sync
```

`sync` catches up, then stays connected and saves new messages until you press Ctrl+C.
For a one-off catch-up use `hkcli sync --once`. To run it all the time, see
[Run sync as a service](sync.md#run-sync-as-a-service).

## 6. Use it from scripts and agents

```bash
hkcli --json chats list --unread
hkcli --read-only messages search "invoice" --json
HKCLI_READONLY=1 hkcli send text --to Asha --message "x"   # refused
```

Every command accepts `--json`. See [Commands](commands.md) for the output format and exit
codes, and [OpenClaw](openclaw.md) to give your agent access.
