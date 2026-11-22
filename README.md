# hikecli

**Hike in your terminal.** An unofficial, script-friendly command-line client for
[Hike Messenger](https://hikeapp.com). It links to your account the same way
Hike Web does (scan a QR code with your phone), keeps a searchable copy of your
chats in SQLite, and sends messages from the terminal, shell scripts, and AI agents
such as [OpenClaw](https://openclaw.ai).

**Website and docs:** https://vinaycodeaxe.github.io/hikecli/

> hikecli is an independent project, not affiliated with or endorsed by Hike Messenger Ltd.
> It talks to the same private servers Hike Web uses, so a change on Hike's side can break it.
> Using an unofficial client may go against Hike's terms; use it with your own account and at your own risk.

## Install

Needs Python 3.10 or newer.

```bash
uv tool install hikecli        # or: pipx install hikecli
hikecli --version
```

From source: `uv tool install git+https://github.com/vinaycodeaxe/hikecli`
(or clone the repo and run `python -m pip install -e .`).

## Quick start

```bash
hikecli auth                        # shows a QR code; scan it in Hike: Settings > Web QR Login
hikecli chats list                  # your chats, newest first
hikecli messages list --chat Asha   # latest messages in a chat
hikecli messages search "lunch"     # search everything stored locally
hikecli send text --to Asha --message "hi from the terminal"
hikecli sync                        # stay connected and save new messages (Ctrl+C to stop)
```

If the QR code does not scan, use `hikecli auth --qr-format png`, or log in with an
SMS code: `hikecli auth --phone +919876543210`.

## Commands

| Command | What it does |
| --- | --- |
| `auth` / `auth status` / `auth logout` | Log in (QR or `--phone`), show who is logged in, log out |
| `sync` | Catch up, then listen live. `--once` catches up and exits. `--json` prints one event per line |
| `chats list` / `chats mark-read --chat X` | List chats (`--unread`, `--groups`, `--direct`); mark one as read |
| `messages list` / `search` / `show` | Read, full-text search, or inspect one message |
| `history backfill --chat X` | Fetch older messages for one chat |
| `send text --to X --message M` | Send text (`--message -` reads stdin, `--reply-to ID` replies) |
| `send file --to X --file PATH` | Send any file as a document (`--caption`) |
| `contacts list` / `contacts refresh` | Show or re-download contacts |
| `groups list` / `groups info --group X` | List groups; show members |
| `media download --chat X --id ID` | Save a photo, video, audio or file |
| `doctor [--connect]` | Check the install, store and login |

`--to`, `--chat` and `--group` accept a name, a phone number with country code, or an id.
Add `--json` to any command for `{"success", "data", "error"}` output.
Full reference: [Commands](https://vinaycodeaxe.github.io/hikecli/commands.html).

## Use with OpenClaw

hikecli ships an OpenClaw skill in [`skills/hikecli`](skills/hikecli/SKILL.md), like the
`wacli` skill for WhatsApp. On the machine that runs your OpenClaw Gateway:

```bash
uv tool install hikecli
hikecli auth                                  # scan the QR with your phone
openclaw skills install @vinaycodeaxe/hikecli --global   # from ClawHub
# or, from a checkout of this repo:
openclaw skills install ./skills/hikecli --global
```

Then ask your agent things like "search my Hike chats for the lunch plan" or
"send Asha on Hike: running 10 minutes late". The agent confirms before sending.
Details: [OpenClaw guide](https://vinaycodeaxe.github.io/hikecli/openclaw.html).

## Safety

- `--read-only` (or `HIKECLI_READONLY=1`) blocks anything that sends to Hike or changes your saved data.
- `session.json` in the store (default `~/.hikecli`) holds your login. Anyone with that file
  can read and send messages as you. Treat it like a password.
- `hikecli auth logout` ends the session on Hike's side and deletes the saved login.
  The local message copy stays until you delete the store folder.

## Limits

- History comes from Hike's server, 50 messages per request; `sync` fetches recent
  messages for up to 50 changed chats per run, and `history backfill` fetches older ones.
- Photos and videos are sent as documents. Voice notes, stickers, GIFs, view-once media
  and calls are received and stored, but not sent.
- Read and delivery ticks are tracked for one-to-one chats only.

## How it works

hikecli speaks the same protocol as Hike Web: QR login over a WebSocket, a REST API for
history, contacts and media, and MQTT over WebSocket for live messages. See
[How it works](https://vinaycodeaxe.github.io/hikecli/how-it-works.html).

## Development

```bash
python -m pip install -e ".[test,docs]"
python -m pytest -q              # runs against a local fake of Hike's servers
python scripts/build_site.py     # builds the website into dist/site
```

See [AGENTS.md](AGENTS.md) for repo rules and [docs/release.md](docs/release.md) for releasing.

## License

[MIT](LICENSE) © 2026 Vinay Chaudhary. The OpenClaw skill is also published on ClawHub,
where all skills are distributed under MIT-0.
