---
title: Troubleshooting
description: "Fix common hikecli problems: QR codes that will not scan, expired logins, locked stores and failed sends."
---

# Troubleshooting

Start with:

```bash
hikecli doctor            # install, store, database and login
hikecli doctor --connect  # also tests Hike's API and live relay
```

## The QR code does not scan

- Make the terminal window bigger, or the font smaller, so the whole code fits.
- Run `hikecli auth --qr-format png` and scan the image instead.
- Scan from Hike's **Settings > Web QR Login**, not from the phone's camera app.
- Or skip the QR code: `hikecli auth --phone +<country code><number>`.

## "not logged in" or exit code 3

The saved login is missing, or Hike no longer accepts it. Run `hikecli auth status`, then
`hikecli auth --force` to link again.

## "another hikecli process is already running sync"

Only one `sync` or `auth` can use a store at a time. Stop the other one, or leave it
running: reading and sending work alongside it. The lock goes away when that process ends.

## "Hike did not confirm delivery"

hikecli tried three times without a confirmation. The message may still have arrived, so
check with `hikecli messages list --chat <chat>` (or in the Hike app) before sending again.

## "matches several chats" or "no chat or contact matches"

Use the chat id from `hikecli chats list`. For someone you have never chatted with, use their
phone number with `+` and the country code. Run `hikecli sync --once` if a new chat is missing.

## Search finds nothing

Search only covers what is stored. Run `hikecli sync --once`, or
[`history backfill`](history.md) for older messages. `doctor` also reports whether SQLite
FTS5 is available.

## Hike changed something

hikecli follows Hike Web. If commands that used to work start failing with HTTP errors, check
for an update (`uv tool upgrade hikecli`) and the
[issue tracker](https://github.com/vinaycodeaxe/hikecli/issues). If Hike starts asking for a
newer app version, see [Version string](how-it-works.md#version-string).

## Reporting a bug

Open an issue with the command, the error and the `hikecli doctor` output. Never paste
`session.json`, tokens or private messages.
