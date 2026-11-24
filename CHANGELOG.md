# Changelog

All notable changes to hkcli are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). The release workflow copies the
section for each tagged version into the GitHub release notes.

## [Unreleased]

### Added

- Gotchas in the docs, the OpenClaw skill and the channel plugin's readme: logging out in the
  Hike app ends hkcli's login, the app holds one number at a time, Hike limits SMS codes, and the
  channel's PATH, group id, routing and slow-model pitfalls.

### Fixed

- OpenClaw channel 0.1.2: changes to bindings, agents and session settings now apply to the next
  Hike message. Before, they needed a restart of the Hike channel.

## [0.3.1] - 2026-11-23

### Changed

- Updated dependencies, including the SQLite driver (modernc.org/sqlite 1.63.0, SQLite 3.54.1)
  and golang.org/x/sys 0.50.0. Building from source now needs Go 1.26 or newer, the oldest Go
  release still supported; `go install` downloads it automatically when needed. Release
  downloads are unaffected.

### Security

- Includes the fix for GO-2026-5024 in golang.org/x/sys (Windows only). hkcli did not call the
  affected function.

## [0.3.0] - 2026-11-22

### Added

- OpenClaw channel: a plugin, `@vinaycodeaxe/hkcli-channel` on ClawHub, lets people chat with an
  OpenClaw agent on Hike, like OpenClaw's WhatsApp channel. The agent gets its own Hike number
  (linked with `openclaw channels login --channel hike`), new people are approved with pairing
  codes, and in groups the agent answers when @mentioned. See the OpenClaw channel page.
- `sync --json` message events now include `chat_type` (`one_to_one` or `group`), the
  message's @mentions (`mentions`) and `mentions_me`, for bots and the OpenClaw channel.

### Changed

- The OpenClaw skill no longer tells the agent that Hike cannot be a channel, and keeps it away
  from the channel's hkcli store.

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
