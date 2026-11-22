---
title: How it works
description: "The Hike Web protocol hkcli speaks: QR login, the REST API, and MQTT over WebSocket."
---

# How it works

Hike has no public API. hkcli speaks the same protocol as the Hike Web app at
`web.hikeapp.com`. Everything on this page was read from Hike Web's public JavaScript and
checked against the live servers.

## Login

- **QR.** hkcli opens
  `wss://session.hikeapp.com/session/v1/qr-login?webDeviceId=<uuid>&appId=hike-messenger`.
  The server sends `{"type": "QR_GENERATED", "qrToken": ..., "expiresInSeconds": 120}`, and
  the QR code encodes `qrToken` as-is. The client sends `PING` every 20 seconds and
  `REFRESH_QR` 20 seconds before the code expires. When the phone approves, the server sends
  `LOGIN_CONFIRMED` with `accessToken`, `refreshToken`, `userId` and `profile`.
- **SMS code.** `POST /auth/v1/initiate` with the number and `deviceType: WEB`, then
  `POST /auth/v1/verify` with the code.
- **Device id.** A random UUID per store (`device.json`), like the `hike_device_id` Hike Web
  keeps in browser storage.

## REST API

The base URL is `https://api-ext.hikeapp.com`. Every request carries
`Authorization: <access token>` (without "Bearer"), `X-User-Id`, `X-App-Id: hike-messenger`,
`X-App-Version` and `X-Build-Number`. A `401` triggers one `POST /auth/v1/refresh` and a retry.

| Endpoint | Used for |
| --- | --- |
| `GET /msg-history/v1/conversations/web` | Chat list with unread counts and the last message |
| `GET /msg-history/v1/messages/fetch/{chat}` | History pages, newest first (`batchSize`, `startMessageId`); each item wraps the original packet as `messageJson` |
| `GET /contact/v1/contacts/getcontacts` | Contacts, paged and incremental (`syncedAt`) |
| `POST /auth/v1/user/profiles/sync`, `POST /auth/v1/user/profiles/by-phone` | Names and numbers for user ids |
| `GET /group/v1/groups/{id}` | Group details and members |
| `GET /media/v1/{fileId}/download-url` | Signed links to byte-range chunks of a file |
| `POST /media/v1/upload/initiate`, chunk `PUT`s, `POST /media/v1/upload/{session}/complete` | Uploads |

## Live messages

Hike's relay is MQTT 3.1.1 over WebSocket at `wss://relay-ws.hikeapp.com/mqtt`:

- Client id `web_<userId>_<epoch ms>`, user name = user id, password = access token,
  keep-alive 25 seconds.
- The client subscribes to `u:<userId>` and publishes everything it sends to the topic
  `<userId>`: messages and receipts at QoS 1, typing and presence at QoS 0.
- **Hike extension:** the broker's PUBACK carries a payload after the packet id (a 2-byte
  length, then the bytes) holding the server message id. Standard MQTT libraries reject such a
  PUBACK, so hkcli has a small MQTT client of its own (`internal/mqtt`).

Packets are JSON with short keys:

| Key | Meaning |
| --- | --- |
| `t` | Type: `m` message, `dr`/`fdr` delivered, `mr`/`fmr` read, `r` reaction, `ma` edit, delete, star or pin, `ca` chat action, `v` call, `gca` group change, `inv` invite, `st`/`et` typing, `fg`/`bg`/`pr` presence, `ds` data sync |
| `st` | Subtype, for example `text`, `img`, `vid`, `audio`, `file`, `stk`, `gif`, `loc`, `cn` |
| `f`, `to`, `c` | From, to, chat |
| `i` | Client message id (a UUID) |
| `s` | Server message id, `seq:timestamp` |
| `ts` | Epoch milliseconds |
| `d` | Data, for example `m` text, `fid` file id, `fn` file name, `mime`, `s` size |

A one-to-one chat is identified by the other person's user id, a group by its group id.
Receipts are cumulative: "read up to `seq`" covers every earlier message in that chat.

## Version string

hkcli reports Hike Web's version (1.3.2, build 39). If Hike starts requiring a newer
version, set `HKCLI_APP_VERSION` and `HKCLI_BUILD_NUMBER` to the values in the current
Hike Web bundle until hkcli is updated.
