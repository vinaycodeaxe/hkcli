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
- hkcli 0.4.1 or newer on the Gateway machine.
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

To use an SMS code instead, for a number that is not logged in to any phone's Hike app, run
`hkcli --store ~/.openclaw/hike/default auth --phone +<country code><number>`. Then restart the
Gateway, or start only the Hike channel so other channels keep running:

```bash
openclaw gateway call channels.start --params '{"channel":"hike","accountId":"default"}'
```

The channel keeps its own hkcli store (`~/.openclaw/hike/<account>`), separate from
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
hkcli --store ~/.openclaw/hike/default chats list --groups   # the ID column is the group id
openclaw config set channels.hike.groups '{"<group id>": {"allowFrom": ["*"]}}' --strict-json --merge
```

Use the id; group names do not work here. If the group is not listed yet, send a message in it
and list again.

The agent replies only when @mentioned; set `requireMention: false` for a group to change that.
`allowFrom: ["*"]` lets every member mention it; list Hike user ids to limit who can.

## 5. A separate agent for Hike (optional)

Hike chats go to your default agent. To give them their own agent, for example one with a
faster model so replies take seconds instead of minutes:

```bash
openclaw agents add hiker --workspace ~/.openclaw/workspaces/hiker --model <provider/model> --non-interactive
openclaw agents bind --agent hiker --bind hike
```

Plugin 0.1.2 and newer route the next message to it; 0.1.1 needs a restart of the Hike channel
first. Anyone you allow in a group can talk to this agent, so deny it tools they should not use
(`agents.entries.hiker.tools.deny`).

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
- Direct messages sent while the Gateway is down are answered when it starts again, up to 12
  hours back; group messages from that time are skipped. Chat history from before the first
  start is never answered.
- Like hkcli itself, the channel is unofficial and not affiliated with Hike. Hike could change
  its service or block unofficial clients, so use a number you can afford to lose.

## Gotchas

- **Never log the agent's number out of the Hike app.** That ends the channel's login too. To
  keep the phone's Hike app on your own number, log the agent's number in with an SMS code
  (step 2) and leave it out of the app. See [Auth gotchas](auth.md#gotchas).
- **SMS codes are limited.** After `OTP send limit reached`, wait (for example until the next
  day) instead of asking for more codes.
- **After logging in again, start the channel.** Restart the Gateway, or run `channels.start`
  as in step 2.
- **The Gateway service may not find hkcli.** A service often does not have `~/go/bin` on its
  PATH. Set `channels.hike.cliPath` to hkcli's full path, or install hkcli into a folder on the
  service's PATH (such as `~/.local/bin`).
- **Groups are matched by id, never by name**, and the agent waits for an @mention unless the
  group sets `requireMention: false`.
- **Slow replies come from the agent's model**, not from Hike. A large model with deep thinking
  can take minutes; route Hike to a faster agent (step 5).
- **Routing changes need plugin 0.1.2.** With 0.1.1 a new binding applies only after the Hike
  channel restarts: `channels.stop`, then `channels.start`, with the same params as in step 2.
- **`provenance-invalid` warning on install.** OpenClaw shows it for community plugins from
  ClawHub; the plugin still installs and works.

## Troubleshooting

| Problem | Fix |
| --- | --- |
| `hkcli was not found` or `too old` | Install hkcli 0.4.1 or newer, or set `channels.hike.cliPath` to its full path |
| Messages stay at one tick and get no answer | Update hkcli to 0.4.1 or newer, then restart the Hike channel. If the log warns about `late` messages, the account is also open elsewhere (Hike Web in a browser, another hkcli store) |
| `not linked` | `openclaw channels login --channel hike` |
| Login says the account is running | Stop the Gateway, log in, then start it again |
| `Hike login no longer works` | The number was logged out in the Hike app, or the phone removed this device; log in again, then start the channel |
| `OTP send limit reached` | Too many SMS codes; try again later |
| Nothing happens in a group | List the group under `groups`, set `allowFrom`, and @mention the agent |
| Replies are slow | Use a faster model for Hike (step 5) |
| A new binding is ignored | Update the plugin to 0.1.2, or restart the Hike channel |

`openclaw logs` shows why a message was dropped. Message text is never written to the log.
