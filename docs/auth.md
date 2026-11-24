---
title: Auth
description: "Link hkcli to your Hike account with a QR code or SMS code, check the login, and log out."
---

# Auth

hkcli logs in as a **web device**, exactly like [Hike Web](https://web.hikeapp.com/login).
Your phone stays your main device.

## QR login

```bash
hkcli auth
```

1. hkcli shows a QR code in the terminal.
2. On your phone, open Hike and go to **Settings > Web QR Login**.
3. Scan the code. hkcli saves the login and runs a first sync.

Each code is valid for about two minutes. hkcli asks Hike for a fresh code shortly before
it expires and keeps waiting for up to five minutes (`--timeout SECONDS` changes this).

If the terminal code does not scan:

- `--qr-format png` saves the code as an image (`login-qr.png` in the store) and opens it.
- `--qr-format text` prints the raw code text so you can turn it into a QR image elsewhere.

hkcli always saves `login-qr.png` while it waits and deletes it after login.

## SMS code login

```bash
hkcli auth --phone +919876543210
```

Hike texts you a code; type it when asked. Write the number with `+` and the country code.
You get three tries.

## After login

`auth` runs a first sync: contacts, your chat list, and the newest messages of up to 50 chats.

| Flag | Meaning |
| --- | --- |
| `--no-sync` | Skip the first sync |
| `--max-chats N` | Fetch messages for up to N chats (0 = all) |
| `--batch N` | Messages per history request (default 50) |
| `--force` | Log in again even though a login is saved |

## Check the login

```bash
hkcli auth status
hkcli auth status --check   # also asks Hike whether the login still works
```

## Log out

```bash
hkcli auth logout
```

This tells Hike to end the web session and deletes the saved login. Your local messages stay
in the store until you delete them. `--local-only` deletes the saved login without
contacting Hike.

## Tokens and expiry

The login is an access token and a refresh token in `session.json`. hkcli refreshes the
access token automatically, one process at a time. If Hike rejects the refresh (for example
because the session was ended), commands exit with code 3 and `auth status` says the saved
login no longer works. Run `hkcli auth --force` to link again.

## Gotchas

- **Logging out in the Hike app logs hkcli out too.** When an account is logged out of the Hike
  app, Hike ends that account's web logins, hkcli's included. Commands then exit with code 3. Run
  `hkcli auth --force` to link again.
- **The Hike app holds one number.** Switching the app to another number means logging the first
  one out, which ends hkcli's login for it. To use a second number in hkcli (for example for an
  [OpenClaw agent](openclaw-channel.md)) while the app stays on yours, log that number in with an
  SMS code and keep it out of the app.
- **SMS codes are limited.** Hike sends only a few codes per number, then refuses with a message
  such as `OTP send limit reached`. Stop there and try again later, for example the next day;
  more attempts do not help.

## More than one account

Each store holds one account. Use a separate store per account:

```bash
hkcli --store ~/.hkcli-work auth
hkcli --store ~/.hkcli-work chats list
```

or set `HKCLI_STORE`. If you log a store into a different account, hkcli moves the old
database aside as `hike.db.<old user id>.<time>.bak` instead of mixing the two.
