# hkcli

**Hike in your terminal.** An unofficial, script-friendly command-line client for
[Hike Messenger](https://hikeapp.com). It links to your account the same way Hike Web does
(scan a QR code with your phone), keeps a searchable copy of your chats in SQLite, and sends
messages from the terminal, shell scripts, and AI agents such as [OpenClaw](https://openclaw.ai).
One small binary for macOS, Linux and Windows; nothing else to install.

**Website and docs:** https://vinaycodeaxe.github.io/hkcli/

> hkcli is an independent project, not affiliated with or endorsed by Hike Messenger Ltd.
> It talks to the same private servers Hike Web uses, so a change on Hike's side can break it.
> Using an unofficial client may go against Hike's terms; use it with your own account and at your own risk.

## Install

Download the archive for your system from the
[latest release](https://github.com/vinaycodeaxe/hkcli/releases/latest) and put `hkcli` on
your `PATH`, or build it with Go:

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest
hkcli --version
```

More options: [Install](https://vinaycodeaxe.github.io/hkcli/install.html).

## Quick start

```bash
hkcli auth                        # shows a QR code; scan it in Hike: Settings > Web QR Login
hkcli chats list                  # your chats, newest first
hkcli messages list --chat Asha   # latest messages in a chat
hkcli messages search "lunch"     # search everything stored locally
hkcli send text --to Asha --message "hi from the terminal"
hkcli sync                        # stay connected and save new messages (Ctrl+C to stop)
```

If the QR code does not scan, use `hkcli auth --qr-format png`, or log in with an SMS code:
`hkcli auth --phone +919876543210`.

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
Full reference: [Commands](https://vinaycodeaxe.github.io/hkcli/commands.html).

## Use with OpenClaw

hkcli ships an OpenClaw skill in [`skills/hkcli`](skills/hkcli/SKILL.md), like the `wacli`
skill for WhatsApp. On the machine that runs your OpenClaw Gateway:

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest   # or a release download
hkcli auth                                                 # scan the QR with your phone
openclaw skills install @vinaycodeaxe/hkcli --global       # from ClawHub
# or, from a checkout of this repo:
openclaw skills install ./skills/hkcli --global
```

Then ask your agent things like "search my Hike chats for the lunch plan" or "send Asha on
Hike: running 10 minutes late". The agent confirms before sending.
Details: [OpenClaw guide](https://vinaycodeaxe.github.io/hkcli/openclaw.html).

## Safety

- `--read-only` (or `HKCLI_READONLY=1`) blocks anything that sends to Hike or changes your saved data.
- `session.json` in the store (default `~/.hkcli`) holds your login. Anyone with that file can
  read and send messages as you. Treat it like a password.
- `hkcli auth logout` ends the session on Hike's side and deletes the saved login. The local
  message copy stays until you delete the store folder.

## Limits

- History comes from Hike's server, 50 messages per request; `sync` fetches recent messages
  for up to 50 changed chats per run, and `history backfill` fetches older ones.
- Photos and videos are sent as documents. Voice notes, stickers, GIFs, view-once media and
  calls are received and stored, but not sent.
- Read and delivery ticks are tracked for one-to-one chats only.

## How it works

hkcli speaks the same protocol as Hike Web: QR login over a WebSocket, a REST API for history,
contacts and media, and MQTT over WebSocket for live messages. See
[How it works](https://vinaycodeaxe.github.io/hkcli/how-it-works.html).

## Development

```bash
go test ./...                                   # unit tests
go build -o dist/ ./cmd/hkcli                   # writes dist/hkcli (dist/hkcli.exe on Windows)
python -m pip install -r requirements-dev.txt
python -m pytest -q tests                       # end-to-end tests against a local fake of Hike's servers
python scripts/build_site.py                    # builds the website into dist/site
```

See [AGENTS.md](AGENTS.md) for repo rules and [docs/release.md](docs/release.md) for releasing.

## License

[MIT](LICENSE) © 2026 Vinay Chaudhary. The OpenClaw skill is also published on ClawHub, where
all skills are distributed under MIT-0.
