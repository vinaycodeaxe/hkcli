---
name: hikecli
description: "Read, search and send Hike Messenger chats via the hikecli command-line tool. Use when the user asks about their Hike messages or wants to message someone on Hike."
homepage: https://vinaycodeaxe.github.io/hikecli/
metadata:
  openclaw:
    emoji: "💬"
    homepage: https://vinaycodeaxe.github.io/hikecli/
    requires:
      bins:
        - hikecli
    install:
      - id: uv
        kind: uv
        package: hikecli
        bins:
          - hikecli
        label: Install hikecli (uv)
---

# hikecli

Use `hikecli` when the user asks you to read, search or summarize their Hike chats,
or to send someone a message or file on Hike.
Hike is not an OpenClaw channel: nobody can chat with you over Hike and nothing arrives
on its own. Only touch Hike when the user asks.

## Safety

- Sending needs an explicit recipient and message text from the user. Do not invent or rewrite the text without showing it.
- Before sending, show the resolved recipient (name and id) and the exact text, and wait for the user to confirm.
- If a name matches several chats or anything is unclear, ask instead of guessing.
- Use `--read-only` when you only need to read. It blocks anything that sends to Hike or changes saved data.
- Never print, copy or upload `~/.hikecli/session.json` (it is the login). Do not run `hikecli auth logout` unless asked.
- Messages are private. Quote only what the user needs.

## Login (the user does this)

Login needs the user's phone, so ask the user to run it in a terminal on this machine:

- `hikecli auth`, then scan the QR code in the Hike app: Settings > Web QR Login.
- Headless machine: `hikecli auth --qr-format png` (saves an image), `--qr-format text`,
  or `hikecli auth --phone +<country code><number>` for an SMS code.
- Check: `hikecli auth status --json`. Exit code 3 from any command means the login is missing or expired.

## Fresh data

- `hikecli sync --once` catches up. Run it before reading if results might be stale.
- If it fails with `store_locked`, a background `hikecli sync` is already keeping data fresh; just read.

## Find chats and messages

- `hikecli chats list --json --limit 20` (filters: `--unread`, `--groups`, `--direct`, `--query "name or number"`)
- `hikecli messages list --chat "<name|+phone|id>" --limit 30 --json`
- `hikecli messages search "words" --limit 20 --json` (add `--chat ...`; end a word with `*` to match prefixes)
- `hikecli messages show --chat <chat id> --id <message id> --json`
- Older history: `hikecli history backfill --chat <chat id> --pages 5`

## Send (only after the user confirms)

- Text: `hikecli send text --to "<name|+phone|id>" --message "Running 10 min late"`
- Multiline: pipe the text in with `--message -`, for example
  `printf 'line one\nline two' | hikecli send text --to <id> --message -`
- Reply: add `--reply-to <message id>`
- File: `hikecli send file --to <id> --file /path/report.pdf --caption "Report"`
- Tell the user the returned message id. If hikecli says Hike did not confirm, do not resend on your own: the message may have gone through, so ask the user.

## Other commands

- Contacts: `hikecli contacts list --query "asha" --json`
- Groups: `hikecli groups list --json`, `hikecli groups info --group "<name|id>" --json`
- Media: `hikecli media download --chat <chat id> --id <message id> --out <folder>`
- Mark read: `hikecli chats mark-read --chat <chat id>` (sends a read receipt; only when asked)
- Health: `hikecli doctor --json` (add `--connect` to test the live connection)

## Notes

- `--to`, `--chat` and `--group` accept an id, a phone number with country code, or a unique name.
  When a name is ambiguous, take the id from `chats list --json`.
- Message ids look like `20:1760000001000` (from Hike) or a UUID (just sent, not yet confirmed).
- `--json` prints `{"success", "data", "error"}`. Errors carry `error.code`, for example
  `auth_required`, `ambiguous`, `not_found`, `read_only` or `store_locked`.
- Store: `~/.hikecli` (override with `--store` or `HIKECLI_STORE`); one store per Hike account.
- hikecli is unofficial. If Hike changes its servers, commands can fail until hikecli is updated.
- Docs: https://vinaycodeaxe.github.io/hikecli/
