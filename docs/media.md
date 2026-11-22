---
title: Media
description: "Download photos, videos, audio and files from your chats."
---

# Media

```bash
hikecli media download --chat Asha --id 22:1760000003000
hikecli media download --chat Asha --id 22:1760000003000 --out ~/Downloads
hikecli media download --chat Asha --id 22:1760000003000 --out ./menu.jpg
```

Find the message id with `messages list`, where media shows as `[image]`, `[video]`,
`[audio]` or `[file]`, or with `messages list --json` (`"has_media": true`).

- Without `--out`, files go to `<store>/media/<chat id>/`.
- `--out` can be a folder or a file path.
- File names chosen by other people are cleaned, so they cannot write outside the target folder.
- The saved path is recorded with the message; `messages show --json` shows it as `local_path`.

## How downloads work

Hike keeps media in cloud storage behind short-lived signed links. hikecli asks Hike for the
links, downloads the file in chunks and checks its size. Your Hike login is only sent to
Hike's API, never to the storage links.

To send files, see [Send](send.md#files).
