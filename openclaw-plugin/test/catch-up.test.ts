import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, it } from "node:test";
import {
  collectMissedDirectMessages,
  createHkcliCatchUpSource,
  readStoredChats,
  type CatchUpLimits,
  type CatchUpSource,
} from "../src/catch-up.ts";
import { fakeEnv, fakeHkcli, scratchDir } from "./helpers.ts";

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

/** Like `hkcli messages list`: the newest `pageSize` rows older than `before`, oldest first. */
function pagedSource(chats: Record<string, ReturnType<typeof row>[]>, pageSize: number) {
  const calls: Array<[string, number | undefined]> = [];
  const source: CatchUpSource = {
    listChats: async () =>
      Object.entries(chats)
        .map(([id, rows]) => ({ id, type: "one_to_one", lastTs: Math.max(...rows.map((entry) => entry.ts)) }))
        .sort((a, b) => b.lastTs - a.lastTs),
    listMessages: async (chatId, before) => {
      calls.push([chatId, before]);
      return (chats[chatId] ?? [])
        .filter((entry) => before === undefined || entry.ts < before)
        .sort((a, b) => b.ts - a.ts)
        .slice(0, pageSize)
        .reverse();
    },
  };
  return { source, calls };
}

const limits = (overrides: Partial<CatchUpLimits>): CatchUpLimits => ({
  chatLimit: 100,
  maxChats: 50,
  pageSize: 50,
  maxPages: 6,
  ...overrides,
});

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
    const result = await collectMissedDirectMessages(source, 100);
    assert.deepEqual(listed, ["2002"]);
    assert.equal(result.truncated, false);
    assert.deepEqual(
      result.messages.map((message) => [message.id, message.chatType, message.source]),
      [
        ["d", "direct", "catch-up"],
        ["b", "direct", "catch-up"],
      ],
    );
  });

  it("pages back through a busy chat until the lower bound", async () => {
    const rows = Array.from({ length: 120 }, (_, i) => row(`m${i}`, 1_000 + i * 10));
    const { source, calls } = pagedSource({ "2002": rows }, 50);
    const result = await collectMissedDirectMessages(source, 1_000 + 5 * 10, limits({ pageSize: 50 }));
    assert.equal(result.truncated, false);
    assert.deepEqual(
      result.messages.map((message) => message.id),
      rows.slice(5).map((entry) => entry.id),
    );
    assert.deepEqual(calls, [
      ["2002", undefined],
      ["2002", 1_000 + 70 * 10 + 1],
      ["2002", 1_000 + 21 * 10 + 1],
    ]);
  });

  it("reports when a limit stopped the read early", async () => {
    const rows = Array.from({ length: 120 }, (_, i) => row(`m${i}`, 1_000 + i * 10));
    const paged = await collectMissedDirectMessages(pagedSource({ "2002": rows }, 50).source, 0, limits({ maxPages: 2 }));
    assert.equal(paged.truncated, true);
    // Each page repeats the oldest row of the page before it.
    assert.equal(paged.messages.length, 99);

    const sameTs = Array.from({ length: 60 }, (_, i) => row(`s${i}`, 5_000));
    const stuck = await collectMissedDirectMessages(pagedSource({ "2002": sameTs }, 50).source, 0, limits({}));
    assert.equal(stuck.truncated, true);

    const many = Object.fromEntries(Array.from({ length: 3 }, (_, i) => [`${2000 + i}`, [row(`c${i}`, 100 + i)]]));
    const chats = await collectMissedDirectMessages(pagedSource(many, 50).source, 0, limits({ maxChats: 2 }));
    assert.equal(chats.truncated, true);
    assert.deepEqual(
      chats.messages.map((message) => message.id),
      ["c1", "c2"],
    );
    const full = await collectMissedDirectMessages(pagedSource(many, 50).source, 0, limits({ chatLimit: 3 }));
    assert.equal(full.truncated, true);
    const complete = await collectMissedDirectMessages(pagedSource(many, 50).source, 101, limits({ chatLimit: 3 }));
    assert.equal(complete.truncated, false);
  });

  it("reads chats and messages through hkcli", async () => {
    const scratch = scratchDir();
    try {
      const log = path.join(scratch.dir, "calls.ndjson");
      const env = fakeEnv({
        FAKE_HKCLI_LOG: log,
        FAKE_HKCLI_CHATS: JSON.stringify([
          { id: "2002", type: "one_to_one", last_ts: 2_000 },
          { id: "g3003", type: "group", last_ts: 3_000 },
          { type: "one_to_one" },
        ]),
        FAKE_HKCLI_MESSAGES: JSON.stringify({ "2002": [row("x", 1_500), row("y", 1_999), row("z", 1_998)] }),
      });
      const source = createHkcliCatchUpSource(fakeHkcli, "store", { env, limits: limits({ pageSize: 2 }) });
      const result = await collectMissedDirectMessages(source, 1_600, limits({ pageSize: 2 }));
      assert.deepEqual(
        result.messages.map((message) => message.id),
        ["z", "y"],
      );
      const calls = readFileSync(log, "utf8")
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line));
      assert.ok(calls[0].args.includes("--direct"));
      assert.deepEqual(
        calls.slice(1).map((call) => [call.chat, call.before]),
        [
          ["2002", null],
          ["2002", "1999"],
        ],
      );
    } finally {
      scratch.cleanup();
    }
  });

  it("parses chat rows defensively", () => {
    assert.deepEqual(readStoredChats([{ id: "1", type: "group", last_ts: 5 }, { id: 2 }, null]), [
      { id: "1", type: "group", lastTs: 5 },
    ]);
    assert.deepEqual(readStoredChats({}), []);
  });
});
