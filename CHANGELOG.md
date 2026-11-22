# Changelog

All notable changes to hkcli are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). The release workflow copies the
section for each tagged version into the GitHub release notes.

## [Unreleased]

### Added

- `sync --json` message events now include `chat_type` (`one_to_one` or `group`), the
  message's @mentions (`mentions`) and `mentions_me`, for bots and the OpenClaw channel.

## [0.2.0] - 2026-11-22

First public release of hkcli.

### Changed

- Rewritten in Go and renamed from `hikecli` to `hkcli`. Each platform gets one small
  binary with nothing else to install, and commands start about ten times faster.
- Environment variables are now `HKCLI_*`. The default store is `~/.hkcli`; an existing
  `~/.hikecli` store from the Python prototype is picked up automatically.
- Install from the release downloads (macOS, Linux and Windows, amd64 and arm64) or with
  `go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest`.

### Features

- `hkcli auth`: link to Hike as a web device by scanning a QR code
  (Settings > Web QR Login), or log in with an SMS code (`--phone`).
- `hkcli sync`: catch up over HTTP, then stay connected and save new messages, receipts,
  reactions, edits and deletes as they arrive; reconnects on its own. `--once` for a single
  catch-up, `--json` for one event per line.
- Local SQLite store with full-text search (`messages search`), plus `chats list`,
  `messages list`, `messages show` and `history backfill`.
- `send text` (with replies and stdin input) and `send file`.
- `media download`, `contacts list/refresh`, `groups list/info`, `chats mark-read` and `doctor`.
- `--read-only` / `HKCLI_READONLY=1` to block anything that sends or changes data.
- OpenClaw skill in `skills/hkcli`, and a documentation website built from `docs/`.

## [0.1.0] - 2026-11-22

Python prototype, published as source only under the name `hikecli`.
