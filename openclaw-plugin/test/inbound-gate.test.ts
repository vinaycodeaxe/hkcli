import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  InboundGate,
  type GateMessage,
  type InboundCursorState,
  type InboundCursorStore,
} from "../src/inbound-gate.ts";

function memoryStore(initial?: unknown) {
  const saves: InboundCursorState[] = [];
  const store: InboundCursorStore = {
    load: async () => initial,
    save: async (state) => {
      saves.push(structuredClone(state));
    },
  };
  return { store, saves };
}

const GRACE = 5 * 60_000;
const MINUTE = 60_000;
const HOUR = 60 * 60_000;
const NOW = 1_800_000_000_000;

const dm = (id: string, ts: number): GateMessage => ({ id, fromMe: false, ts, chatType: "direct" });
const group = (id: string, ts: number): GateMessage => ({ id, fromMe: false, ts, chatType: "group" });

describe("InboundGate", () => {
  it("never answers history on the very first start", async () => {
    const { store, saves } = memoryStore();
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    assert.equal(gate.liveFloor, NOW - GRACE);
    assert.deepEqual(saves, [{ v: 2, cursor: NOW, seen: [] }]);
    assert.equal(gate.check(dm("old", NOW - HOUR)), "stale");
    assert.equal(gate.check(dm("recent", NOW - 30_000)), "accept");
  });

  it("drops our own messages and duplicates, including ones still queued", async () => {
    const gate = await InboundGate.open({ store: memoryStore().store, now: () => NOW });
    assert.equal(gate.check({ ...dm("a", NOW), fromMe: true }), "self");
    assert.equal(gate.check(dm("b", NOW)), "accept");
    assert.equal(gate.check(dm("b", NOW)), "duplicate");
    assert.equal(await gate.commit("b"), true);
    assert.equal(gate.check(dm("b", NOW)), "duplicate");
    assert.equal(gate.check(group("g", NOW)), "accept");
    assert.equal(await gate.commit("g"), true);
    assert.equal(gate.check(group("g", NOW)), "duplicate");
    assert.equal(await gate.commit("never-accepted"), true);
  });

  it("saves a handled direct message before the reply, and never group ids", async () => {
    const { store, saves } = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const gate = await InboundGate.open({ store, now: () => NOW, saveDelayMs: 60_000 });
    gate.check(dm("d", NOW - 1_000));
    gate.check(group("g", NOW - 500));
    await gate.commit("g");
    assert.equal(saves.length, 0);
    await gate.commit("d");
    assert.deepEqual(saves, [{ v: 2, cursor: NOW, seen: [["d", NOW - 1_000]] }]);
  });

  it("answers messages that arrived while the gateway was down, up to the age limit", async () => {
    const stoppedAt = NOW - 2 * HOUR;
    const { store } = memoryStore({ v: 2, cursor: stoppedAt, seen: [["handled", stoppedAt - 1_000]] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    assert.equal(gate.liveFloor, stoppedAt - GRACE);
    assert.equal(gate.beginRecovery(), stoppedAt - GRACE);
    assert.equal(gate.check(dm("while-down", stoppedAt + 60_000)), "accept");
    assert.equal(gate.check(dm("handled", stoppedAt - 1_000)), "duplicate");
    assert.equal(gate.check(dm("ancient", stoppedAt - GRACE - 1)), "stale");

    const longAgo = NOW - 20 * HOUR;
    const old = await InboundGate.open({ store: memoryStore({ v: 2, cursor: longAgo, seen: [] }).store, now: () => NOW });
    assert.equal(old.beginRecovery(), NOW - 12 * HOUR);
    assert.equal(old.check(dm("13h", NOW - 13 * HOUR)), "stale");
    assert.equal(old.check(dm("11h", NOW - 11 * HOUR)), "accept");
  });

  it("keeps the cursor behind queued messages and never ahead of the clock", async () => {
    let now = NOW;
    const { store, saves } = memoryStore({ v: 2, cursor: NOW - 10_000, seen: [] });
    const gate = await InboundGate.open({ store, now: () => now, saveDelayMs: 60_000 });
    for (const [id, ts] of [["1", NOW + 600_000], ["2", NOW - 9_000], ["3", NOW - 8_000]] as const) {
      assert.equal(gate.check(dm(id, ts)), "accept");
    }
    await gate.commit("1");
    await gate.commit("3");
    gate.advance(NOW);
    await gate.flush();
    // "2" is still queued, so the cursor stays just before it; a restart offers it again.
    assert.equal(saves.at(-1)?.cursor, NOW - 9_001);
    await gate.commit("2");
    now = NOW + 5_000;
    gate.advance(NOW + 60_000);
    await gate.flush();
    assert.equal(saves.at(-1)?.cursor, NOW + 5_000);
    const count = saves.length;
    gate.advance(NOW);
    await gate.flush();
    assert.equal(saves.length, count);
  });

  it("forgets handled ids by age, so group traffic never makes an old DM look new", async () => {
    let now = NOW;
    const { store, saves } = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const gate = await InboundGate.open({ store, now: () => now, graceMs: GRACE, maxSeen: 10, saveDelayMs: 0 });
    gate.check(dm("monday", NOW + 1_000));
    await gate.commit("monday");
    for (let i = 0; i < 2_000; i += 1) {
      now += 1_000;
      assert.equal(gate.check(group(`g${i}`, now)), "accept");
      await gate.commit(`g${i}`);
    }
    gate.check(dm("late", now));
    await gate.commit("late");
    assert.deepEqual(
      saves.at(-1)?.seen.map(([id]) => id),
      ["monday", "late"],
    );
    assert.equal(gate.check(dm("monday", NOW + 1_000)), "duplicate");

    // Two days later the cursor has moved on: a read-back starts after the old DM.
    now = NOW + 48 * HOUR;
    gate.advance(now - 60_000);
    await gate.flush();
    assert.deepEqual(saves.at(-1)?.seen, []);
    const since = gate.beginRecovery();
    assert.equal(since, now - 60_000 - GRACE);
    assert.equal(gate.check(dm("monday", NOW + 1_000), since), "stale");
  });

  it("remembers ids a running read-back may still offer", async () => {
    let now = NOW;
    const { store, saves } = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const gate = await InboundGate.open({ store, now: () => now, graceMs: GRACE, saveDelayMs: 0 });
    gate.check(dm("a", NOW - 60_000));
    await gate.commit("a");
    const since = gate.beginRecovery();
    now += HOUR;
    gate.advance(now);
    await gate.flush();
    assert.deepEqual(saves.at(-1)?.seen, [["a", NOW - 60_000]]);
    assert.equal(gate.check(dm("a", NOW - 60_000), since), "duplicate");
    gate.endRecovery(since);
    now += 1_000;
    gate.advance(now);
    await gate.flush();
    assert.deepEqual(saves.at(-1)?.seen, []);
  });

  it("raises the read-back floor when too many handled ids must be kept", async () => {
    const { store, saves } = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE, maxSeen: 3 });
    const running = gate.beginRecovery();
    for (let i = 1; i <= 5; i += 1) {
      gate.check(dm(`m${i}`, NOW - 100 + i));
      await gate.commit(`m${i}`);
    }
    const saved = saves.at(-1);
    assert.deepEqual(
      saved?.seen.map(([id]) => id),
      ["m3", "m4", "m5"],
    );
    assert.equal(saved?.floor, NOW - 100 + 3);
    // A read-back that started before the cap dropped "m2" must not offer it again either.
    assert.equal(gate.check(dm("m2", NOW - 100 + 2), running), "stale");
    gate.endRecovery(running);
    const since = gate.beginRecovery();
    assert.equal(since, NOW - 100 + 3);
    assert.equal(gate.check(dm("m2", NOW - 100 + 2), since), "stale");

    const reopened = await InboundGate.open({ store: memoryStore(saved).store, now: () => NOW, graceMs: GRACE });
    assert.equal(reopened.beginRecovery(), NOW - 100 + 3);
  });

  it("never raises the read-back floor above the cursor", async () => {
    const cursor = NOW - MINUTE;
    const { store, saves } = memoryStore({ v: 2, cursor, seen: [] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE, maxSeen: 2 });
    for (const [id, ts] of [["a", NOW - 30_000], ["b", NOW - 20_000], ["c", NOW - 10_000]] as const) {
      gate.check(dm(id, ts));
      await gate.commit(id);
    }
    assert.equal(saves.at(-1)?.floor, cursor);
    assert.equal(gate.beginRecovery(), cursor);

    // State saved by a version that let the floor run ahead is pulled back to the cursor.
    const ahead = await InboundGate.open({
      store: memoryStore({ v: 2, cursor: NOW, floor: Date.UTC(2099, 0, 1), seen: [] }).store,
      now: () => NOW,
      graceMs: GRACE,
    });
    assert.equal(ahead.beginRecovery(), NOW);
  });

  it("answers future-dated messages live, but they cannot switch off recovery", async () => {
    const y2099 = Date.UTC(2099, 0, 1);
    const { store, saves } = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    for (let i = 0; i <= 800; i += 1) {
      assert.equal(gate.check(dm(`spam${i}`, y2099)), "accept");
      await gate.commit(`spam${i}`);
    }
    assert.equal(saves.at(-1)?.floor, undefined);
    assert.deepEqual(saves.at(-1)?.seen, []);
    const since = gate.beginRecovery();
    assert.equal(since, NOW - GRACE);
    assert.equal(gate.check(dm("spam0", y2099), since), "future");
    assert.equal(gate.check(dm("while-down", NOW - 2_000), since), "accept");

    // Ids dated a little ahead are kept (time may catch up with them), but are dropped first.
    const near = memoryStore({ v: 2, cursor: NOW, seen: [] });
    const ahead = await InboundGate.open({ store: near.store, now: () => NOW, graceMs: GRACE, maxSeen: 3 });
    for (let i = 0; i < 4; i += 1) {
      ahead.check(dm(`ahead${i}`, NOW + HOUR + i));
      await ahead.commit(`ahead${i}`);
    }
    ahead.check(dm("real", NOW - 1_000));
    await ahead.commit("real");
    assert.deepEqual(
      near.saves.at(-1)?.seen.map(([id]) => id),
      ["real", "ahead0", "ahead1"],
    );
    assert.equal(near.saves.at(-1)?.floor, undefined);
    assert.equal(ahead.check(dm("ahead9", NOW + HOUR), ahead.beginRecovery()), "future");
  });

  it("reads version 1 state", async () => {
    const { store, saves } = memoryStore({ v: 1, cursor: NOW - 1_000, seen: ["a"] });
    const gate = await InboundGate.open({ store, now: () => NOW, graceMs: GRACE });
    assert.equal(gate.check(dm("a", NOW - 2_000)), "duplicate");
    gate.check(dm("b", NOW - 500));
    await gate.commit("b");
    assert.deepEqual(saves.at(-1), {
      v: 2,
      cursor: NOW - 1_000,
      seen: [
        ["a", NOW - 1_000],
        ["b", NOW - 500],
      ],
    });
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
    assert.equal(gate.liveFloor, NOW - GRACE);
    assert.equal(errors.length, 2);
    gate.check(dm("x", NOW));
    assert.equal(await gate.commit("x"), false);
    assert.equal(errors.length, 3);
    const garbage = await InboundGate.open({ store: memoryStore({ v: 7 }).store, now: () => NOW });
    assert.equal(garbage.cursorValue, NOW);
  });
});
