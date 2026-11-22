# Changelog

All notable changes to hikecli are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). The release workflow copies the
section for each tagged version into the GitHub release notes.

## [Unreleased]

## [0.1.0] - 2026-11-22

First public release.

### Added

- `hikecli auth`: link to Hike as a web device by scanning a QR code
  (Settings > Web QR Login), or log in with an SMS code (`--phone`).
- `hikecli sync`: catch up over HTTP, then stay connected and save new messages,
  receipts, reactions, edits and deletes as they arrive; reconnects on its own.
  `--once` for a single catch-up, `--json` for one event per line.
- Local SQLite store with full-text search (`messages search`), plus
  `chats list`, `messages list`, `messages show` and `history backfill`.
- `send text` (with replies and stdin input) and `send file`.
- `media download`, `contacts list/refresh`, `groups list/info`,
  `chats mark-read` and `doctor`.
- `--read-only` / `HIKECLI_READONLY=1` to block anything that sends or changes data.
- OpenClaw skill in `skills/hikecli`.
- Documentation website built from `docs/`.
