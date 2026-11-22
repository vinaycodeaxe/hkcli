import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  collectMissedDirectMessages,
  createHkcliCatchUpSource,
  readStoredChats,
  type CatchUpSource,
} from "../src/catch-up.ts";
import { fakeEnv, fakeHkcli } from "./helpers.ts";

function row(id: string, ts: number, extra: Record<string, unknown> = {}) {
  return {
    id,
    server_id: id,
    client_id: `c-${id}`,
    chat: "2002",
    chat_name: "Asha",
    ts,
    from_me: false,
    sender: "2002",
    sender_name: "Asha",
    kind: "text",
    text: `text ${id}`,
    deleted: false,
    ...extra,
  };
}

describe("collectMissedDirectMessages", () => {
  it("returns newer direct messages from others, oldest first, and skips groups", async () => {
    const listed: string[] = [];
    const source: CatchUpSource = {
      listChats: async () => [
        { id: "2002", type: "one_to_one", lastTs: 500 },
        { id: "2003", type: "one_to_one", lastTs: 50 },
        { id: "g1", type: "group", lastTs: 900 },
      ],
      listMessages: async (chatId) => {
        listed.push(chatId);
        return [
          row("a", 90),
          row("b", 300),
          row("c", 200, { from_me: true }),
          row("d", 150),
          row("e", 400, { deleted: true }),
          { nonsense: true },
        ];
      },
    };
    const messages = await collectMissedDirectMessages(source, 100);
    assert.deepEqual(listed, ["2002"]);
    assert.deepEqual(
      messages.map((message) => [message.id, message.chatType, message.source]),
      [
        ["d", "direct", "catch-up"],
        ["b", "direct", "catch-up"],
      ],
    );
  });

  it("reads chats and messages through hkcli", async () => {
    const env = fakeEnv({
      FAKE_HKCLI_CHATS: JSON.stringify([
        { id: "2002", type: "one_to_one", last_ts: 2_000 },
        { id: "g3003", type: "group", last_ts: 3_000 },
        { type: "one_to_one" },
      ]),
      FAKE_HKCLI_MESSAGES: JSON.stringify({ "2002": [row("x", 1_500), row("y", 1_999)] }),
    });
    const source = createHkcliCatchUpSource(fakeHkcli, "store", { env });
    const messages = await collectMissedDirectMessages(source, 1_600);
    assert.deepEqual(
      messages.map((message) => message.id),
      ["y"],
    );
  });

  it("parses chat rows defensively", () => {
    assert.deepEqual(readStoredChats([{ id: "1", type: "group", last_ts: 5 }, { id: 2 }, null]), [
      { id: "1", type: "group", lastTs: 5 },
    ]);
    assert.deepEqual(readStoredChats({}), []);
  });
});
