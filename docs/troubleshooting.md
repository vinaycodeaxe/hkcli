---
title: Troubleshooting
description: "Fix common hkcli problems: QR codes that will not scan, expired logins, locked stores and failed sends."
---

# Troubleshooting

Start with:

```bash
hkcli doctor            # install, store, database and login
hkcli doctor --connect  # also tests Hike's API and live relay
```

## The QR code does not scan

- Make the terminal window bigger, or the font smaller, so the whole code fits.
- Run `hkcli auth --qr-format png` and scan the image instead.
- Scan from Hike's **Settings > Web QR Login**, not from the phone's camera app.
- Or skip the QR code: `hkcli auth --phone +<country code><number>`.

## "not logged in" or exit code 3

The saved login is missing, or Hike no longer accepts it. Run `hkcli auth status`, then
`hkcli auth --force` to link again.

## The login stopped working after using the Hike app

Logging the account out of the Hike app, or switching the app to another number, ends hkcli's
login too. Run `hkcli auth --force`. To keep the app on a different number, log this one in with
an SMS code instead; see [Auth gotchas](auth.md#gotchas).

## "OTP send limit reached"

Hike sends no more SMS codes to this number for a while. Try again later, for example the next
day, or use QR login if the Hike app is logged in to this number.

## Messages stay at one tick while sync runs

Update to hkcli 0.4.1 or newer, then restart `sync`. Older versions could open a second relay
connection for a `send`, after which Hike could stop delivering messages to the running `sync`
until it reconnected; see [Sending while sync runs](sync.md#sending-while-sync-runs). If it still
happens, look for another session of the same account: Hike Web open in a browser, or another
hkcli store. `sync --json` then reports `late` messages in its `sync` events after reconnecting.

## "relay_unavailable"

Another hkcli process on this store kept its relay connection busy for about 25 seconds, so the
command sent nothing. Try again. If it keeps happening, check for a stuck hkcli process.

## "another hkcli process is already running sync"

Only one `sync` or `auth` can use a store at a time. Stop the other one, or leave it
running: reading and sending work alongside it. The lock goes away when that process ends.

## "Hike did not confirm delivery"

hkcli tried three times without a confirmation. The message may still have arrived, so
check with `hkcli messages list --chat <chat>` (or in the Hike app) before sending again.

## "matches several chats" or "no chat or contact matches"

Use the chat id from `hkcli chats list`. For someone you have never chatted with, use their
phone number with `+` and the country code. Run `hkcli sync --once` if a new chat is missing.

## Search finds nothing

Search only covers what is stored. Run `hkcli sync --once`, or
[`history backfill`](history.md) for older messages.

## Hike changed something

hkcli follows Hike Web. If commands that used to work start failing with HTTP errors, check
for a [new release](https://github.com/vinaycodeaxe/hkcli/releases/latest) and the
[issue tracker](https://github.com/vinaycodeaxe/hkcli/issues). If Hike starts asking for a
newer app version, see [Version string](how-it-works.md#version-string).

## Reporting a bug

Open an issue with the command, the error and the `hkcli doctor` output. Never paste
`session.json`, tokens or private messages.
