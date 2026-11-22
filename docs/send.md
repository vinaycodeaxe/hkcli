---
title: Send
description: "Send texts, replies and files, and how hikecli matches recipients."
---

# Send

```bash
hikecli send text --to Asha --message "hi"
hikecli send text --to +919876543210 --message "hi"
hikecli send file --to Family --file ./photo.jpg --caption "from the trip"
```

## Recipients

`--to` (and `--chat` and `--group` in other commands) is matched in this order:

1. A chat or user **id**, as shown by `chats list`.
2. A **phone number**. hikecli looks for a saved contact or chat with that number. If there
   is none and the number has `+` and a country code, it asks Hike who owns it, so you can
   message people you have not chatted with yet.
3. A **name**: first an exact match (ignoring case), then a partial match.

If more than one chat matches, hikecli stops and lists them with their ids. Nothing is sent.

## Text

- `--message` is sent exactly as written.
- `--message -` reads the text from stdin, which is the easiest way to send several lines:

```bash
printf 'line one\nline two\n' | hikecli send text --to Asha --message -
```

## Replies

```bash
hikecli send text --to Asha --message "works for me" --reply-to 23:1760000004000
```

The message you reply to must be in the local store, so run `sync` first.

## Files

```bash
hikecli send file --to Asha --file ./report.pdf
hikecli send file --to Asha --file ./clip.mov --mime video/quicktime --caption "see 0:30"
```

- Any file up to 1 GB.
- Every file is sent as a document, photos and videos included: they arrive as attachments
  without a preview.
- The type comes from the file name; `--mime` overrides it.

## Delivery

hikecli waits for Hike to confirm each message and prints its id. If Hike does not confirm,
it retries up to three times with the same message id, as Hike Web does. If it still fails,
the error says the message **may or may not** have been sent; check with `messages list`
before sending again.

Sent messages are saved to the local store right away. Delivery and read ticks arrive while
`sync` runs.
