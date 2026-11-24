---
title: OpenClaw skill
description: "Let your OpenClaw agent read, search and send Hike messages through the hkcli skill."
---

# OpenClaw skill

hkcli includes an [OpenClaw](https://openclaw.ai) skill, the same pattern OpenClaw uses
with `wacli` for WhatsApp. The skill teaches your agent the hkcli commands, and the agent
runs them on the machine where your OpenClaw Gateway runs.

With it, you can ask things like:

- "Any unread Hike messages? Summarize them."
- "Search my Hike chats for the address Ravi sent."
- "Send Asha on Hike: running 10 minutes late." The agent shows the recipient and the text
  and waits for your OK before sending.

The skill only uses Hike when you ask, with the Hike account hkcli is logged in to (usually
yours). To let people chat with your agent over Hike, the way OpenClaw's WhatsApp channel
works, use the [OpenClaw channel](openclaw-channel.md) instead; you can have both.

## 1. Install hkcli on the Gateway machine

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest   # or a release download, see Install
hkcli --version
```

OpenClaw can also do this for you: the skill declares a `go` installer, which the Skills UI
offers when `hkcli` is missing (OpenClaw installs Go first if needed). If your agent runs in
a sandbox, hkcli must be installed inside the sandbox too.

## 2. Log in on that machine

Login needs your phone, so do it yourself in a terminal on that machine (over SSH is fine):

```bash
hkcli auth
```

The terminal QR code works over SSH. You can also use `hkcli auth --qr-format png` and
open the image, or `hkcli auth --phone +919876543210` for an SMS code. Check the result
with `hkcli auth status`. Afterwards, do not log this number out of the Hike app: that logs
hkcli out too (see [Auth gotchas](auth.md#gotchas)).

## 3. Install the skill

From ClawHub:

```bash
openclaw skills install @vinaycodeaxe/hkcli --global
```

Or from a checkout of the repository:

```bash
git clone https://github.com/vinaycodeaxe/hkcli
openclaw skills install ./hkcli/skills/hkcli --global
```

`--global` puts the skill in `~/.openclaw/skills`, so every agent on the machine can use
it; leave it out to install into the current workspace only. Copying
`skills/hkcli/SKILL.md` to `~/.openclaw/skills/hkcli/SKILL.md` by hand works too.

Check that OpenClaw sees it, then start a new session:

```bash
openclaw skills check
```

The skill only loads when `hkcli` is on the Gateway's `PATH`.

## 4. Keep history fresh (optional)

The agent runs `hkcli sync --once` when it needs fresh data. To save messages all the
time, run `hkcli sync` as a background service; see
[Run sync as a service](sync.md#run-sync-as-a-service). The agent's reads and sends keep
working while that service runs.

## Make an agent read-only

If an agent should read Hike but never send, set `HKCLI_READONLY` for the skill in
`~/.openclaw/openclaw.json`:

```json5
{
  skills: {
    entries: {
      hkcli: { env: { HKCLI_READONLY: "1" } },
    },
  },
}
```

hkcli then refuses `send`, `chats mark-read` and anything else that changes data. This
setting applies to agent runs on the host; for sandboxed runs, set the variable in the
sandbox instead. To hide Hike from an agent completely, leave `hkcli` out of that agent's
skill allowlist, or set `hkcli: { enabled: false }`.

## Privacy

When the agent reads your messages, they are sent to the AI model it uses. Use read-only
mode and agent allowlists to limit what each agent sees, and never let an agent share
`~/.hkcli/session.json`.

## Updating

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest   # or download the latest release
openclaw skills update @vinaycodeaxe/hkcli --global
```
