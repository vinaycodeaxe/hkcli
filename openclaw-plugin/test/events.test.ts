import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { inboundBody, parseStoredMessage, parseSyncEvent, type HikeInboundMessage } from "../src/events.ts";

const dm = {
  event: "message",
  chat: "2002",
  chat_name: "Asha",
  chat_type: "one_to_one",
  id: "40:1760000060000",
  server_id: "40:1760000060000",
  client_id: "c-40",
  sender: "2002",
  sender_name: "Asha",
  from_me: false,
  ts: 1760000060000,
  kind: "text",
  text: "are you there?",
  file_name: null,
  reply_to: null,
  mentions: [],
  mentions_me: false,
};

function message(overrides: Partial<HikeInboundMessage>): HikeInboundMessage {
  return {
    id: "1",
    chatId: "2002",
    chatType: "direct",
    senderId: "2002",
    fromMe: false,
    ts: 1,
    kind: "text",
    mentions: [],
    mentionsMe: false,
    source: "live",
    ...overrides,
  };
}

describe("parseSyncEvent", () => {
  it("reads direct messages", () => {
    const event = parseSyncEvent(dm);
    assert.equal(event?.type, "message");
    assert.deepEqual(event?.type === "message" ? event.message : undefined, {
      id: "40:1760000060000",
      serverId: "40:1760000060000",
      clientId: "c-40",
      chatId: "2002",
      chatName: "Asha",
      chatType: "direct",
      senderId: "2002",
      senderName: "Asha",
      fromMe: false,
      ts: 1760000060000,
      kind: "text",
      text: "are you there?",
      fileName: undefined,
      replyTo: undefined,
      mentions: [],
      mentionsMe: false,
      source: "live",
    });
  });

  it("reads group messages with native mentions", () => {
    const event = parseSyncEvent({
      ...dm,
      chat: "g3003",
      chat_type: "group",
      sender: "2003",
      text: "@Bot lunch?",
      mentions: [{ uid: "1001", phone: "+919800000001", start: 0, length: 4 }, { uid: null, phone: "+91", start: 9, length: 1 }],
      mentions_me: true,
    });
    assert.ok(event?.type === "message");
    assert.equal(event.message.chatType, "group");
    assert.equal(event.message.mentionsMe, true);
    assert.deepEqual(event.message.mentions, [
      { uid: "1001", phone: "+919800000001", start: 0, length: 4 },
      { uid: undefined, phone: "+91", start: 9, length: 1 },
    ]);
  });

  it("ignores messages without a chat type or id", () => {
    const { chat_type: _type, ...withoutType } = dm;
    assert.deepEqual(parseSyncEvent(withoutType), { type: "ignored", name: "message" });
    assert.deepEqual(parseSyncEvent({ ...dm, id: null, server_id: null, client_id: null }), {
      type: "ignored",
      name: "message",
    });
  });

  it("reads lifecycle events and error envelopes", () => {
    assert.deepEqual(parseSyncEvent({ event: "connected" }), { type: "connected" });
    assert.deepEqual(parseSyncEvent({ event: "sync", messages: 3, chats: 2 }), { type: "sync", messages: 3 });
    assert.deepEqual(parseSyncEvent({ event: "disconnected", reason: "eof" }), { type: "disconnected", reason: "eof" });
    assert.deepEqual(parseSyncEvent({ event: "typing", chat: "2002" }), { type: "ignored", name: "typing" });
    assert.deepEqual(
      parseSyncEvent({ success: false, data: null, error: { code: "auth_required", message: "run hkcli auth" } }),
      { type: "error", code: "auth_required", message: "run hkcli auth" },
    );
    assert.equal(parseSyncEvent("text"), undefined);
    assert.equal(parseSyncEvent({ hello: 1 }), undefined);
  });
});

describe("parseStoredMessage", () => {
  it("reads `messages list` rows and drops deleted ones", () => {
    const row = { ...dm, event: undefined, chat_type: undefined, deleted: false, state: "delivered" };
    assert.equal(parseStoredMessage(row, "direct")?.source, "catch-up");
    assert.equal(parseStoredMessage({ ...row, deleted: true }, "direct"), undefined);
    assert.equal(parseStoredMessage({ ...row, from_me: true }, "direct")?.fromMe, true);
  });
});

describe("inboundBody", () => {
  it("uses the text of text messages", () => {
    assert.equal(inboundBody(message({ text: "  hi  " })), "hi");
    assert.equal(inboundBody(message({ text: "" })), undefined);
    assert.equal(inboundBody(message({ text: undefined })), undefined);
  });

  it("describes media with a short placeholder", () => {
    assert.equal(inboundBody(message({ kind: "image", fileName: "menu.jpg" })), "[photo: menu.jpg]");
    assert.equal(inboundBody(message({ kind: "image", text: "menu photo" })), "[photo] menu photo");
    assert.equal(inboundBody(message({ kind: "document", fileName: "notes.pdf", text: "see" })), "[file: notes.pdf] see");
    assert.equal(inboundBody(message({ kind: "sticker" })), "[sticker]");
    assert.equal(inboundBody(message({ kind: "poll" })), "[poll]");
  });

  it("skips calls, system notices and invites", () => {
    assert.equal(inboundBody(message({ kind: "call", text: "call ended" })), undefined);
    assert.equal(inboundBody(message({ kind: "system", text: "joined" })), undefined);
    assert.equal(inboundBody(message({ kind: "invite", text: "group invite" })), undefined);
  });
});
