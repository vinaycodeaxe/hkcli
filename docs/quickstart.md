---
title: Quickstart
description: "Log in, sync, search and send your first Hike message from the terminal in five minutes."
---

# Quickstart

## 1. Install

```bash
uv tool install hikecli
hikecli --version
```

Other options are on [Install](install.md).

## 2. Link your Hike account

```bash
hikecli auth
```

`auth` prints a QR code. On your phone, open Hike, go to **Settings > Web QR Login**, and
scan it. Once the phone confirms, hikecli saves the login and downloads your chats and
recent messages.

If the code does not scan, run `hikecli auth --qr-format png` to open it as an image, or
log in with an SMS code: `hikecli auth --phone +919876543210`. More on [Auth](auth.md).

## 3. Look around

```bash
hikecli chats list
hikecli messages list --chat Asha
hikecli messages search "lunch"
```

`--chat` takes a name, a phone number with country code, or the id shown by `chats list`.

## 4. Send a message

```bash
hikecli send text --to Asha --message "hi from the terminal"
hikecli send file --to Asha --file ./notes.pdf --caption "notes"
```

See [Send](send.md) for replies, piped text and how recipients are matched.

## 5. Keep it up to date

```bash
hikecli sync
```

`sync` catches up, then stays connected and saves new messages until you press Ctrl+C.
For a one-off catch-up use `hikecli sync --once`. To run it all the time, see
[Run sync as a service](sync.md#run-sync-as-a-service).

## 6. Use it from scripts and agents

```bash
hikecli --json chats list --unread
hikecli --read-only messages search "invoice" --json
HIKECLI_READONLY=1 hikecli send text --to Asha --message "x"   # refused
```

Every command accepts `--json`. See [Commands](commands.md) for the output format and exit
codes, and [OpenClaw](openclaw.md) to give your agent access.
