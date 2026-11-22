---
title: OpenClaw
description: "Let your OpenClaw agent read, search and send Hike messages through the hikecli skill."
---

# OpenClaw

hikecli includes an [OpenClaw](https://openclaw.ai) skill, the same pattern OpenClaw uses
with `wacli` for WhatsApp. The skill teaches your agent the hikecli commands, and the agent
runs them on the machine where your OpenClaw Gateway runs.

With it, you can ask things like:

- "Any unread Hike messages? Summarize them."
- "Search my Hike chats for the address Ravi sent."
- "Send Asha on Hike: running 10 minutes late." The agent shows the recipient and the text
  and waits for your OK before sending.

Hike is not an OpenClaw channel. People cannot chat with your agent over Hike, and the agent
only uses Hike when you ask.

## 1. Install hikecli on the Gateway machine

```bash
uv tool install hikecli
hikecli --version
```

OpenClaw can also do this for you: the skill declares a `uv` installer, which the Skills UI
offers when `hikecli` is missing. If your agent runs in a sandbox, hikecli must be installed
inside the sandbox too.

## 2. Log in on that machine

Login needs your phone, so do it yourself in a terminal on that machine (over SSH is fine):

```bash
hikecli auth
```

The terminal QR code works over SSH. You can also use `hikecli auth --qr-format png` and
open the image, or `hikecli auth --phone +919876543210` for an SMS code. Check the result
with `hikecli auth status`.

## 3. Install the skill

From ClawHub:

```bash
openclaw skills install @vinaycodeaxe/hikecli --global
```

Or from a checkout of the repository:

```bash
git clone https://github.com/vinaycodeaxe/hikecli
openclaw skills install ./hikecli/skills/hikecli --global
```

`--global` puts the skill in `~/.openclaw/skills`, so every agent on the machine can use
it; leave it out to install into the current workspace only. Copying
`skills/hikecli/SKILL.md` to `~/.openclaw/skills/hikecli/SKILL.md` by hand works too.

Check that OpenClaw sees it, then start a new session:

```bash
openclaw skills check
```

The skill only loads when `hikecli` is on the Gateway's `PATH`.

## 4. Keep history fresh (optional)

The agent runs `hikecli sync --once` when it needs fresh data. To save messages all the
time, run `hikecli sync` as a background service; see
[Run sync as a service](sync.md#run-sync-as-a-service). The agent's reads and sends keep
working while that service runs.

## Make an agent read-only

If an agent should read Hike but never send, set `HIKECLI_READONLY` for the skill in
`~/.openclaw/openclaw.json`:

```json5
{
  skills: {
    entries: {
      hikecli: { env: { HIKECLI_READONLY: "1" } },
    },
  },
}
```

hikecli then refuses `send`, `chats mark-read` and anything else that changes data. This
setting applies to agent runs on the host; for sandboxed runs, set the variable in the
sandbox instead. To hide Hike from an agent completely, leave `hikecli` out of that agent's
skill allowlist, or set `hikecli: { enabled: false }`.

## Privacy

When the agent reads your messages, they are sent to the AI model it uses. Use read-only
mode and agent allowlists to limit what each agent sees, and never let an agent share
`~/.hikecli/session.json`.

## Updating

```bash
uv tool upgrade hikecli
openclaw skills update @vinaycodeaxe/hikecli --global
```
