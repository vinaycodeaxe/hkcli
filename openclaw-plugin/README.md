# Hike channel for OpenClaw

Chat with your OpenClaw agent on [Hike Messenger](https://hike.in). The agent gets its own Hike
account (use a second number, not your personal one). People message that number; the agent
answers DMs from approved people and, in groups, only when someone @mentions it.

The plugin drives [hkcli](https://github.com/vinaycodeaxe/hkcli), an unofficial Hike command line
client. It is not affiliated with Hike.

Full guide: https://vinaycodeaxe.github.io/hkcli/openclaw-channel.html

## Requirements

- OpenClaw 2026.11.6 or newer, with a local Gateway.
- hkcli 0.3.0 or newer on the Gateway host:
  `go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest`, or a binary from
  https://github.com/vinaycodeaxe/hkcli/releases.
- A phone with the Hike account the agent should use.

## Install and log in

```bash
openclaw plugins install clawhub:@vinaycodeaxe/hkcli-channel
openclaw config set channels.hike.enabled true
openclaw channels login --channel hike
openclaw gateway restart
```

`channels login` shows a QR code in the terminal. On the agent's phone open
Hike > Settings > Web QR Login and scan it. To log in with an SMS code instead, run
`hkcli --store "<store dir>" auth --phone +<country code><number>`, then restart the Gateway. The
store dir is printed by `channels login`; by default it is `~/.openclaw/hike/default`.

Check the account: `openclaw channels status --channel hike --probe`.

## Direct messages and pairing

By default (`dmPolicy: "pairing"`) an unknown sender gets a short pairing code and nothing reaches
the agent. Approve them on the Gateway host:

```bash
openclaw pairing list hike
openclaw pairing approve hike <code>
```

To also send them a confirmation message, approve the request in the Control UI
(Settings > Channels > DM access requests) with notification turned on. Other policies:
`"allowlist"` (only `allowFrom`), `"open"` (anyone; requires `allowFrom: ["*"]`), `"disabled"`.

## Groups

Groups are off until you list them (`groupPolicy: "allowlist"`). The agent replies only when
@mentioned, unless you set `requireMention: false`. Find a group's id with
`hkcli --store ~/.openclaw/hike/default chats list` (group rows have type `group`).

```bash
openclaw config set channels.hike.groups '{"<group id>": {"allowFrom": ["*"]}}' --strict-json --merge
```

`allowFrom: ["*"]` lets every member trigger the agent with a mention; list Hike user ids to limit
it. `groupPolicy: "open"` accepts every group the account is in (still mention-gated).

## Configuration

All keys live under `channels.hike` (or `channels.hike.accounts.<id>` for extra accounts).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | | Set `true` to turn the channel on. |
| `cliPath` | `hkcli` | hkcli command or path. |
| `storeDir` | `<OpenClaw state dir>/hike/<account>` | hkcli store for this account (login and message history). Never share it. |
| `dmPolicy` | `pairing` | `pairing`, `allowlist`, `open` or `disabled`. |
| `allowFrom` | `[]` | Hike user ids allowed to DM the agent. |
| `groupPolicy` | `allowlist` | `allowlist`, `open` or `disabled`. |
| `groupAllowFrom` | `[]` | Hike user ids allowed to trigger the agent in any allowed group. |
| `groups.<id or *>` | | `enabled`, `requireMention` (default `true`), `allowFrom`, `tools`, `skills`, `systemPrompt`. |
| `textChunkLimit` | `4000` | Longer replies are split into several messages. |

Authorization always uses Hike user ids, never display names.

Extra accounts: add `channels.hike.accounts.work = {}` and log in with
`openclaw channels login --channel hike --account work`. Each account gets its own store, and
top-level keys other than `storeDir` are shared defaults. Once linked, the default account keeps
running next to the named ones.

## What works in 0.1.0

- Text in and out, in direct and group chats.
- Photos, files, stickers and other media arrive as a short placeholder such as `[photo: a.jpg]`;
  the agent cannot see media yet and sends text only (links for attachments).
- Direct messages that arrive while the Gateway is down are answered after it restarts, up to
  12 hours back. Hike history from before the first start is never answered.

## Troubleshooting

- `hkcli was not found` / `too old`: install hkcli 0.3.0+ or set `channels.hike.cliPath`.
- `not linked`: run `openclaw channels login --channel hike`.
- Login says the account is running: stop it first with
  `openclaw gateway call channels.stop --params '{"channel":"hike","accountId":"default"}'`
  (or stop the Gateway), log in, then start it again with `channels.start`.
- `Hike login no longer works`: the phone logged this device out; log in again.
- Nothing happens in a group: list the group under `groups`, add `allowFrom`, and @mention the
  agent. The Gateway log shows why a message was dropped.
- Logs: `openclaw logs` (message text is never logged at info level).

## License

MIT. Copyright (c) 2026 Vinay Chaudhary.
