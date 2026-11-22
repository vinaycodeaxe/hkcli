---
title: Overview
description: "hikecli links to your Hike account as a web device, keeps a searchable copy of your chats in SQLite, and sends messages from the terminal, scripts and AI agents such as OpenClaw."
---

# hikecli

An unofficial, script-friendly command-line client for Hike Messenger. It links to your
account the same way Hike Web does (you scan a QR code with your phone), keeps a
searchable copy of your chats in SQLite on your machine, and sends messages from the
terminal, shell scripts and AI agents such as [OpenClaw](openclaw.md).

## Why hikecli

- **Local copy, fast search.** Messages are saved in SQLite with a full-text index, so `messages search` works offline and answers in milliseconds.
- **Live sync.** `hikecli sync` stays connected, saves new messages, receipts, reactions and edits as they arrive, and reconnects on its own.
- **Stable output.** Tables for people, `--json` for scripts, and one JSON event per line from `sync --json`. Progress and errors go to stderr, so pipes stay clean.
- **Built for agents.** `--read-only` (or `HIKECLI_READONLY=1`) blocks anything that sends to Hike or changes saved data. An OpenClaw skill is included.
- **Small and portable.** Python 3.10+, three small dependencies, macOS, Linux and Windows.

## Pick your path

- **Trying it.** Read [Install](install.md), then [Quickstart](quickstart.md).
- **Using it from OpenClaw.** Read [OpenClaw](openclaw.md).
- **Keeping history fresh.** Read [Sync](sync.md), including how to run it as a background service.
- **Searching old chats.** Read [Messages](messages.md) and [History](history.md).
- **Sending from scripts.** Read [Send](send.md).
- **Building on the data.** Read [Commands](commands.md) for JSON output and exit codes, and [Store](store.md) for the database.
- **Curious how it works.** Read [How it works](how-it-works.md).

## Status

Version 0.1, tested against the Hike Messenger app relaunched in 2026 by Hike Messenger Ltd.
Hike's web protocol is not a published API, so expect occasional breakage when Hike changes it.
The [changelog](https://github.com/vinaycodeaxe/hikecli/blob/main/CHANGELOG.md) lists every release.

## Out of scope

- Calls, and sending stickers, GIFs, voice notes or view-once media (they are received and stored).
- A guaranteed complete copy of old history; Hike's server decides what is available.
- Acting as an OpenClaw chat channel, where people talk to your agent over Hike.

## Disclaimer

hikecli is an independent project. It is not affiliated with, endorsed by or supported by
Hike Messenger Ltd, and "Hike" belongs to its owner. Using an unofficial client may go
against Hike's terms of service. Use it with your own account and at your own risk.
