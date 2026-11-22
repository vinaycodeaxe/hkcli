---
title: OpenClaw channel
description: "Chat with your OpenClaw agent on Hike, like OpenClaw's WhatsApp channel: the agent gets its own Hike number."
---

# OpenClaw channel

The Hike channel lets people chat with your [OpenClaw](https://openclaw.ai) agent on Hike, the
same way OpenClaw's WhatsApp channel works:

- The agent gets its own Hike account. Use a second phone number, not your personal one.
- You, and people you approve, message that number from your normal Hike, and the agent replies.
- In group chats the agent answers only when someone @mentions it.

The channel is an OpenClaw plugin, `@vinaycodeaxe/hkcli-channel`, published on ClawHub. It runs
hkcli in the background on the machine where your OpenClaw Gateway runs. For the opposite
direction, your agent reading and sending *your* Hike messages when you ask, see the
[OpenClaw skill](openclaw.md).

## What you need

- OpenClaw 2026.11.6 or newer.
- hkcli 0.3.0 or newer on the Gateway machine.
- A phone with Hike set up for the agent's number.

## 1. Install hkcli and the plugin

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest   # or a release download, see Install
openclaw plugins install clawhub:@vinaycodeaxe/hkcli-channel
openclaw config set channels.hike.enabled true
```

## 2. Link the agent's Hike account

```bash
openclaw channels login --channel hike
openclaw gateway restart
```

`channels login` shows a QR code in the terminal; this works over SSH too. On the agent's
phone, open Hike > Settings > Web QR Login and scan it. Check the result with
`openclaw channels status --channel hike --probe`.

To use an SMS code instead, run
`hkcli --store ~/.openclaw/hike/default auth --phone +<country code><number>`, then restart the
Gateway. The channel keeps its own hkcli store (`~/.openclaw/hike/<account>`), separate from
your personal `~/.hkcli`.

## 3. Approve people

When someone messages the agent for the first time, they get a short pairing code and nothing
reaches the agent. Approve them on the Gateway machine:

```bash
openclaw pairing list hike
openclaw pairing approve hike <code>
```

Approving the request in the Control UI also sends them a confirmation message. Other options
for `channels.hike.dmPolicy`: `allowlist` (only the Hike user ids in `allowFrom`), `open`
(anyone; needs `allowFrom: ["*"]`) and `disabled`.

## 4. Groups (optional)

Groups are off until you list them. Add the agent's number to a Hike group, find the group id,
then allow the group:

```bash
hkcli --store ~/.openclaw/hike/default chats list   # group rows have type "group"
openclaw config set channels.hike.groups '{"<group id>": {"allowFrom": ["*"]}}' --strict-json --merge
```

The agent replies only when @mentioned; set `requireMention: false` for a group to change that.
`allowFrom: ["*"]` lets every member mention it; list Hike user ids to limit who can.

## Settings

All keys live under `channels.hike` (or `channels.hike.accounts.<id>` for more accounts; log
those in with `openclaw channels login --channel hike --account <id>`).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | | `true` turns the channel on |
| `cliPath` | `hkcli` | hkcli command or full path |
| `storeDir` | `~/.openclaw/hike/<account>` | hkcli store for this account; never share it |
| `dmPolicy` | `pairing` | `pairing`, `allowlist`, `open` or `disabled` |
| `allowFrom` | `[]` | Hike user ids allowed to message the agent |
| `groupPolicy` | `allowlist` | `allowlist`, `open` (every group the account is in) or `disabled` |
| `groupAllowFrom` | `[]` | Hike user ids allowed to trigger the agent in any allowed group |
| `groups.<id or *>` | | `enabled`, `requireMention` (default `true`), `allowFrom`, `tools`, `skills`, `systemPrompt` |
| `textChunkLimit` | `4000` | Longer replies are split into several messages |

Access is always checked by Hike user id, never by display name.

## Limits

- Text only for now. Photos, files and stickers reach the agent as a short note such as
  `[photo: a.jpg]`, and the agent replies with text.
- Direct messages sent while the Gateway is down are answered when it starts again; group
  messages from that time are skipped. Chat history from before the first start is never
  answered.
- Like hkcli itself, the channel is unofficial and not affiliated with Hike. Hike could change
  its service or block unofficial clients, so use a number you can afford to lose.

## Troubleshooting

| Problem | Fix |
| --- | --- |
| `hkcli was not found` or `too old` | Install hkcli 0.3.0 or newer, or set `channels.hike.cliPath` |
| `not linked` | `openclaw channels login --channel hike` |
| Login says the account is running | Stop the Gateway, log in, then start it again |
| `Hike login no longer works` | The phone logged this device out; log in again |
| Nothing happens in a group | List the group under `groups`, set `allowFrom`, and @mention the agent |

`openclaw logs` shows why a message was dropped. Message text is never written to the log.
