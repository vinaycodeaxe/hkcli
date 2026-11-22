import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { InboundGate, type InboundCursorState, type InboundCursorStore } from "../src/inbound-gate.ts";

function memoryStore(initial?: unknown) {
  const saves: InboundCursorState[] = [];
  const store: InboundCursorStore = {
    load: async () => initial as InboundCursorState | undefined,
    save: async (state) => {
      saves.push(structuredClone(state));
    },
  };
  return { store, saves };
}

const GRACE = 5 * 60_000;
const NOW = 1_800_000_000_000;

describe("InboundGate", () => {
  it("never answers history on the very first start", async () => {
    const { store, saves } = memoryStore();
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    assert.equal(gate.threshold, NOW - GRACE);
    assert.deepEqual(saves, [{ v: 1, cursor: NOW, seen: [] }]);
    assert.equal(gate.check({ id: "old", fromMe: false, ts: NOW - 60 * 60_000 }), "stale");
    assert.equal(gate.check({ id: "recent", fromMe: false, ts: NOW - 30_000 }), "accept");
  });

  it("drops our own messages and duplicates, including ones still queued", async () => {
    const gate = await InboundGate.open({ store: memoryStore().store, now: () => NOW });
    assert.equal(gate.check({ id: "a", fromMe: true, ts: NOW }), "self");
    assert.equal(gate.check({ id: "b", fromMe: false, ts: NOW }), "accept");
    assert.equal(gate.check({ id: "b", fromMe: false, ts: NOW }), "duplicate");
    gate.complete("b");
    assert.equal(gate.check({ id: "b", fromMe: false, ts: NOW }), "duplicate");
    gate.release("never-accepted");
  });

  it("answers messages that arrived while the gateway was down", async () => {
    const stoppedAt = NOW - 2 * 60 * 60_000;
    const { store } = memoryStore({ v: 1, cursor: stoppedAt, seen: ["handled"] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    assert.equal(gate.threshold, stoppedAt - GRACE);
    assert.equal(gate.check({ id: "while-down", fromMe: false, ts: stoppedAt + 60_000 }), "accept");
    assert.equal(gate.check({ id: "handled", fromMe: false, ts: stoppedAt - 1_000 }), "duplicate");
    assert.equal(gate.check({ id: "ancient", fromMe: false, ts: stoppedAt - GRACE - 1 }), "stale");
  });

  it("persists handled ids and a cursor that never passes queued messages or the clock", async () => {
    let now = NOW;
    const { store, saves } = memoryStore({ v: 1, cursor: NOW - 10_000, seen: [] });
    const gate = await InboundGate.open({ store, now: () => now, maxSeen: 2, saveDelayMs: 60_000 });
    assert.equal(saves.length, 0);
    for (const [id, ts] of [["1", NOW + 600_000], ["2", NOW - 9_000], ["3", NOW - 8_000], ["4", NOW - 7_000]] as const) {
      assert.equal(gate.check({ id, fromMe: false, ts }), "accept");
    }
    gate.complete("1");
    gate.complete("3");
    gate.complete("4");
    await gate.flush();
    // "2" is still queued, so the cursor stays just before it; a restart offers it again.
    assert.deepEqual(saves.at(-1), { v: 1, cursor: NOW - 9_001, seen: ["3", "4"] });
    gate.release("2");
    now = NOW + 5_000;
    gate.advance(NOW + 60_000);
    await gate.flush();
    assert.equal(saves.at(-1)?.cursor, NOW + 5_000);
    gate.advance(NOW);
    await gate.flush();
    assert.equal(saves.length, 2);
  });

  it("ignores unreadable state and reports store errors without throwing", async () => {
    const errors: unknown[] = [];
    const failing: InboundCursorStore = {
      load: async () => {
        throw new Error("load failed");
      },
      save: async () => {
        throw new Error("save failed");
      },
    };
    const gate = await InboundGate.open({ store: failing, now: () => NOW, onError: (error) => errors.push(error) });
    assert.equal(gate.threshold, NOW - 5 * 60_000);
    assert.equal(errors.length, 2);
    const garbage = await InboundGate.open({ store: memoryStore({ v: 7 }).store, now: () => NOW });
    assert.equal(garbage.cursorValue, NOW);
  });
});
