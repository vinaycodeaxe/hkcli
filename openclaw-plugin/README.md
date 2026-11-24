# Hike channel for OpenClaw

Chat with your OpenClaw agent on [Hike Messenger](https://hike.in). The agent gets its own Hike
account (use a second number, not your personal one). People message that number; the agent
answers DMs from approved people and, in groups, only when someone @mentions it.

The plugin drives [hkcli](https://github.com/vinaycodeaxe/hkcli), an unofficial Hike command line
client. It is not affiliated with Hike.

Full guide: https://vinaycodeaxe.github.io/hkcli/openclaw-channel.html

## Requirements

- OpenClaw 2026.11.6 or newer, with a local Gateway.
- hkcli 0.4.1 or newer on the Gateway host:
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
Hike > Settings > Web QR Login and scan it. To log in with an SMS code instead (for a number that
is not in any phone's Hike app), run `hkcli --store "<store dir>" auth --phone +<country code><number>`,
then restart the Gateway or start only this channel:
`openclaw gateway call channels.start --params '{"channel":"hike","accountId":"default"}'`. The
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
`hkcli --store ~/.openclaw/hike/default chats list --groups` (the ID column); group names do not
work. If the group is not listed yet, send a message in it and list again.

```bash
openclaw config set channels.hike.groups '{"<group id>": {"allowFrom": ["*"]}}' --strict-json --merge
```

`allowFrom: ["*"]` lets every member trigger the agent with a mention; list Hike user ids to limit
it. `groupPolicy: "open"` accepts every group the account is in (still mention-gated).

## Choosing the agent

Hike chats go to your default agent. To send them to another one, for example an agent with a
faster model: `openclaw agents bind --agent <agent id> --bind hike`. The next message follows the
new binding (0.1.1 needed a restart of the Hike channel). Group members can talk to that agent
too, so deny it tools they should not use.

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

## What works in 0.2.0

- Text in and out, in direct and group chats.
- Photos, files, stickers and other media arrive as a short placeholder such as `[photo: a.jpg]`;
  the agent cannot see media yet and sends text only (links for attachments).
- Direct messages that arrive while the Gateway is down are answered after it restarts, up to
  12 hours back. Hike history from before the first start is never answered; after a logout, or
  when another Hike number is linked, the channel starts fresh the same way.
- Changes to bindings, agents and session settings apply to the next message without a restart.
- Replies go out over the channel's own Hike connection (hkcli 0.4.1), so Hike keeps delivering
  new messages after the agent answers. If the Gateway log warns about `late` messages, Hike
  stopped delivering anyway: the account is probably also open elsewhere (Hike Web in a browser,
  another hkcli store), and the agent did not see group messages from that time.

## Gotchas

- Never log the agent's number out of the Hike app: Hike then ends the channel's login too. To
  keep the phone's app on your own number, log the agent's number in with an SMS code and leave
  it out of the app.
- Hike sends only a few SMS codes per number. After `OTP send limit reached`, wait (for example
  until the next day) instead of asking for more.
- After logging in again, start the channel (restart the Gateway, or `channels.start` as above).
- The Gateway service may not have `~/go/bin` on its PATH: set `cliPath` to hkcli's full path.
- Groups are matched by id, never by name, and need an @mention by default.
- Slow replies come from the agent's model, not Hike: route Hike to a faster agent.
- On Windows the Gateway stops hkcli at once (Windows has no Ctrl+C signal for it), so a reply
  that was going out at that moment may be lost.
- OpenClaw may warn `provenance-invalid` when installing community plugins from ClawHub; the
  plugin still installs and works.

## Troubleshooting

- `hkcli was not found` / `too old`: install hkcli 0.4.1+ or set `channels.hike.cliPath`.
- Messages stay at one tick and get no answer: update hkcli to 0.4.1 or newer, then restart the
  channel. Older versions could send a reply over a second Hike connection, after which Hike could
  stop delivering to the channel.
- `not linked`: run `openclaw channels login --channel hike`.
- Login says the account is running: stop it first with
  `openclaw gateway call channels.stop --params '{"channel":"hike","accountId":"default"}'`
  (or stop the Gateway), log in, then start it again with `channels.start`.
- `Hike login no longer works`: the number was logged out in the Hike app, or the phone removed
  this device; log in again, then start the channel.
- Nothing happens in a group: list the group under `groups`, add `allowFrom`, and @mention the
  agent. The Gateway log shows why a message was dropped.
- Logs: `openclaw logs` (message text is never logged at info level).

## License

MIT. Copyright (c) 2026 Vinay Chaudhary.
