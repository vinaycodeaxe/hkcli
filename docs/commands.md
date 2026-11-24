---
title: Commands
description: "Every hkcli command and flag, the JSON output format, exit codes and environment variables."
---

# Commands

## Global options

These work before or after the command name, for example `hkcli --json chats list` or
`hkcli chats list --json`:

| Option | Meaning |
| --- | --- |
| `--store DIR` | Store folder (default `~/.hkcli`, or `HKCLI_STORE`) |
| `--json` | Print JSON (see below) |
| `--read-only` | Refuse anything that sends to Hike or changes saved data |

`hkcli --version` prints the version.

## Command list

| Command | What it does | Page |
| --- | --- | --- |
| `auth` | Log in with a QR code (`--phone` for an SMS code) | [Auth](auth.md) |
| `auth status` | Show the saved login (`--check` asks Hike) | [Auth](auth.md#check-the-login) |
| `auth logout` | End the web session and delete the login | [Auth](auth.md#log-out) |
| `sync` | Catch up and stay connected | [Sync](sync.md) |
| `chats list` | List chats | [Messages](messages.md#chats) |
| `chats mark-read --chat X` | Send a read receipt | [Messages](messages.md#mark-a-chat-as-read) |
| `messages list --chat X` | Read a chat | [Messages](messages.md#read-a-chat) |
| `messages search QUERY` | Full-text search | [Messages](messages.md#search) |
| `messages show --chat X --id ID` | One message in detail | [Messages](messages.md#one-message) |
| `history backfill --chat X` | Fetch older messages | [History](history.md) |
| `send text --to X --message M` | Send text | [Send](send.md) |
| `send file --to X --file PATH` | Send a file | [Send](send.md#files) |
| `media download --chat X --id ID` | Save an attachment | [Media](media.md) |
| `contacts list`, `contacts refresh` | Contacts | [Contacts & groups](contacts.md) |
| `groups list`, `groups info --group X` | Groups | [Contacts & groups](contacts.md#groups) |
| `doctor` | Check the setup (`--connect` tests the live connection) | [Troubleshooting](troubleshooting.md) |

Run `hkcli <command> --help` to see every flag.

## Read-only mode

`--read-only`, or `HKCLI_READONLY` set to anything except an empty value, `0`, `false` or
`no`, blocks `auth`, `auth logout`, `sync`, `history backfill`, `chats mark-read`,
`send text`, `send file`, `media download` and `contacts refresh`. Reading commands,
`auth status`, `groups info` and `doctor` still work.

## JSON output

With `--json`, every command prints one JSON object:

```json
{"success": true, "data": {"...": "..."}, "error": null}
```

On failure:

```json
{"success": false, "data": null, "error": {"code": "ambiguous", "message": "'asha' matches 2 chats/contacts: ..."}}
```

`sync --json` is the exception: it prints one event per line (see [Sync](sync.md#output)).

| Error code | Meaning |
| --- | --- |
| `auth_required` | Not logged in, or the login expired (exit code 3) |
| `already_logged_in` | `auth` without `--force` while a login is saved |
| `not_found` | No matching chat, contact, message or file |
| `ambiguous` | A name matched more than one chat |
| `invalid_input` | A bad flag value, an empty message or a bad phone number |
| `read_only` | Blocked by read-only mode |
| `store_locked` | Another `sync` or `auth` is using this store |
| `no_media` | The message has nothing to download |
| `login_failed` | The QR or SMS login did not complete |
| `relay_error` | The live connection to Hike failed; a message may or may not have been sent |
| `relay_unavailable` | Another hkcli process kept this store's relay connection busy; nothing was sent |
| `error` | An unexpected failure, such as a disk error |
| anything else | Hike's own error code, or `hike_error` |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Error (see the message or `error.code`) |
| 2 | Wrong command or flags |
| 3 | Login needed: run `hkcli auth` |
| 130 | Interrupted with Ctrl+C |

## Environment variables

| Variable | Meaning |
| --- | --- |
| `HKCLI_STORE` | Store folder |
| `HKCLI_READONLY` | Read-only mode |
| `HKCLI_APP_VERSION`, `HKCLI_BUILD_NUMBER` | App version reported to Hike (default: Hike Web 1.3.2, build 39) |
| `HKCLI_API_BASE`, `HKCLI_SESSION_WS`, `HKCLI_MQTT_WS` | Server addresses, used by the tests |
