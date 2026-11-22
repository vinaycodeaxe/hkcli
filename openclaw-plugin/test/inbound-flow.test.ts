import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { CatchUpResult } from "../src/catch-up.ts";
import type { HikeInboundMessage } from "../src/events.ts";
import { InboundFlow, type InboundCommit } from "../src/inbound-flow.ts";
import { InboundGate, type InboundCursorState } from "../src/inbound-gate.ts";

const GRACE = 5 * 60_000;
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const NOW = 1_800_000_000_000;

function message(id: string, ts: number, chatType: "direct" | "group" = "direct"): HikeInboundMessage {
  return {
    id,
    chatId: chatType === "direct" ? "2002" : "g3003",
    chatType,
    senderId: "2002",
    fromMe: false,
    ts,
    kind: "text",
    text: id,
    mentions: [],
    mentionsMe: false,
    source: "live",
  };
}

/**
 * What outlives one account run: the wall and monotonic clocks, the plugin's saved cursor and
 * hkcli's message store.
 */
type World = { now: number; mono: number; state?: InboundCursorState; stored: HikeInboundMessage[] };

/** Time passing while the host is awake: both clocks move. */
function elapse(world: World, ms: number): void {
  world.now += ms;
  world.mono += ms;
}

type RunOptions = {
  readBack?: (since: number, signal: AbortSignal) => Promise<CatchUpResult>;
  handle?: (message: HikeInboundMessage, commit: InboundCommit) => Promise<void>;
};

/** One account run wired like the gateway, with a per-chat queue that skips work after a stop. */
async function startRun(world: World, options: RunOptions = {}) {
  const abort = new AbortController();
  const clock = () => world.now;
  const gate = await InboundGate.open({
    store: {
      load: async () => world.state,
      save: async (state) => {
        world.state = structuredClone(state);
      },
    },
    now: clock,
    graceMs: GRACE,
    saveDelayMs: 0,
  });
  const handled: string[] = [];
  const reads: number[] = [];
  const warnings: string[] = [];
  const restarts: number[] = [];
  const chains = new Map<string, Promise<void>>();
  const flow = new InboundFlow({
    gate,
    readBack:
      options.readBack ??
      (async (since) => {
        reads.push(since);
        // Worst case: the store hands back more than asked for; the gate still decides.
        return { messages: [...world.stored], truncated: false };
      }),
    schedule: (key, task) => {
      const run = (chains.get(key) ?? Promise.resolve()).then(async () => {
        if (!abort.signal.aborted) {
          await task();
        }
      });
      chains.set(key, run);
    },
    handle:
      options.handle ??
      (async (inbound, commit) => {
        if (abort.signal.aborted) {
          return;
        }
        await commit();
        handled.push(inbound.id);
      }),
    restartSync: () => restarts.push(world.now),
    signal: abort.signal,
    log: { info: () => {}, warn: (line) => warnings.push(line) },
    now: clock,
    monotonicNow: () => world.mono,
  });
  const settle = async () => {
    for (let turn = 0; turn < 5; turn += 1) {
      await Promise.all(chains.values());
      await new Promise((resolve) => setImmediate(resolve));
    }
  };
  const stop = async () => {
    abort.abort();
    await settle();
    await flow.stop();
  };
  return { gate, flow, handled, reads, warnings, restarts, settle, stop };
}

describe("InboundFlow", () => {
  it("never answers an old direct message again after a reconnect, whatever came in between", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    const run = await startRun(world);
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();

    const monday = message("monday", world.now + 1_000);
    world.stored.push(monday);
    elapse(world, 2_000);
    run.flow.received(monday);
    await run.settle();

    // Two days of busy group chat; the cursor moves every minute.
    for (let minute = 0; minute < 2 * 24 * 60; minute += 1) {
      elapse(world, MINUTE);
      run.flow.received(message(`g${minute}`, world.now, "group"));
      await run.settle();
      run.flow.tick();
    }

    // A short relay drop: hkcli fetched one missed message, and the store is read back.
    run.flow.connectionLost();
    const missed = message("missed", world.now - 30_000);
    world.stored.push(missed);
    elapse(world, 10_000);
    run.flow.synced(1);
    await run.settle();

    assert.deepEqual(
      run.handled.filter((id) => !id.startsWith("g")),
      ["monday", "missed"],
    );
    assert.equal(run.reads.length, 2);
    assert.equal(run.reads[1], world.now - 10_000 - GRACE);
  });

  it("answers a stored message the last run never got to, even when hkcli fetched nothing", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    let finishA: () => void = () => {};
    const aInFlight = new Promise<void>((resolve) => {
      finishA = resolve;
    });
    const handled: string[] = [];
    const first = await startRun(world, {
      handle: async (inbound, commit) => {
        await commit();
        if (inbound.id === "a") {
          await aInFlight;
        }
        handled.push(inbound.id);
      },
    });
    first.flow.childStarted();
    first.flow.synced(0);
    await first.settle();
    const a = message("a", world.now + 1_000);
    const b = message("b", world.now + 2_000);
    world.stored.push(a, b);
    elapse(world, 3_000);
    first.flow.received(a);
    first.flow.received(b);
    await new Promise((resolve) => setImmediate(resolve));
    elapse(world, MINUTE);
    first.flow.tick();

    // Restart while the agent still answers "a"; "b" waits behind it in the chat's queue.
    const stopped = first.stop();
    finishA();
    await stopped;
    assert.deepEqual(handled, ["a"]);
    assert.equal(world.state?.cursor, b.ts - 1);

    // hkcli already stored "b", so its first catch-up reports nothing new.
    const second = await startRun(world);
    second.flow.childStarted();
    second.flow.synced(0);
    await second.settle();
    assert.deepEqual(second.handled, ["b"]);
  });

  it("keeps the cursor when the account stops during a read-back", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    const run = await startRun(world, {
      readBack: async (_since, signal) =>
        await new Promise<CatchUpResult>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new Error("hkcli stopped")), { once: true });
        }),
    });
    run.flow.childStarted();
    run.flow.synced(4);
    elapse(world, MINUTE);
    run.flow.tick();
    await run.stop();
    assert.equal(world.state?.cursor, NOW);
    assert.deepEqual(run.warnings, []);
  });

  it("retries a failed read-back and holds the cursor until one succeeds", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    let failing = true;
    const reads: number[] = [];
    const run = await startRun(world, {
      readBack: async (since) => {
        reads.push(since);
        if (failing) {
          throw new Error("chats list failed");
        }
        return { messages: world.stored.filter((stored) => stored.ts >= since), truncated: false };
      },
    });
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();
    world.stored.push(message("late", NOW + 1_000));
    elapse(world, MINUTE);
    run.flow.tick();
    await run.settle();
    assert.equal(reads.length, 2);
    assert.equal(run.gate.cursorValue, NOW);
    assert.equal(run.warnings.length, 2);

    failing = false;
    elapse(world, MINUTE);
    run.flow.tick();
    await run.settle();
    assert.equal(reads.length, 3);
    assert.deepEqual(run.handled, ["late"]);
    run.flow.tick();
    assert.equal(run.gate.cursorValue, world.now);
  });

  it("reads the store back for every new hkcli process and every catch-up that fetched messages", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    let release: (result: CatchUpResult) => void = () => {};
    const reads: number[] = [];
    const run = await startRun(world, {
      readBack: async (since) => {
        reads.push(since);
        if (reads.length === 2) {
          return await new Promise<CatchUpResult>((resolve) => {
            release = resolve;
          });
        }
        return { messages: [], truncated: false };
      },
    });
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();
    run.flow.connectionLost();
    run.flow.synced(0);
    await run.settle();
    assert.equal(reads.length, 1);

    run.flow.connectionLost();
    run.flow.synced(2);
    await run.settle();
    assert.equal(reads.length, 2);
    // Another catch-up lands while that read-back still runs: it gets its own afterwards.
    run.flow.connectionLost();
    elapse(world, MINUTE);
    run.flow.synced(1);
    await run.settle();
    assert.equal(reads.length, 2);
    release({ messages: [], truncated: true });
    await run.settle();
    assert.equal(reads.length, 3);
    assert.equal(run.warnings.length, 1);
    run.flow.tick();
    assert.equal(run.gate.cursorValue, world.now);

    run.flow.connectionLost();
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();
    assert.equal(reads.length, 4);
  });

  it("still answers direct messages that arrived while the host slept", async () => {
    // Linux and macOS stop the monotonic clock during suspend; other hosts may keep it running.
    for (const monotonicCountsSleep of [false, true]) {
      const evening = Date.UTC(2026, 10, 22, 23, 0);
      const world: World = { now: evening, mono: 0, stored: [] };
      const run = await startRun(world);
      run.flow.childStarted();
      run.flow.synced(0);
      await run.settle();
      elapse(world, MINUTE);
      run.flow.tick();
      const beforeSleep = run.gate.cursorValue;
      assert.equal(beforeSleep, evening + MINUTE);

      // Eight hours asleep: no ticks, and hkcli's connection dies without it noticing yet.
      world.now += 8 * HOUR;
      if (monotonicCountsSleep) {
        world.mono += 8 * HOUR;
      }
      const night = [message("01:01", beforeSleep + 2 * HOUR), message("05:01", beforeSleep + 6 * HOUR)];

      // The first tick after waking comes before hkcli reports the dead connection.
      elapse(world, 30_000);
      run.flow.tick();
      assert.equal(run.restarts.length, 1);
      assert.equal(run.gate.cursorValue, beforeSleep);

      // The restarted hkcli catches up, storing the night's messages, and the store is read back.
      run.flow.connectionLost();
      run.flow.childStarted();
      world.stored.push(...night);
      run.flow.synced(night.length);
      await run.settle();
      assert.deepEqual(run.handled, ["01:01", "05:01"]);
      assert.equal(run.reads.at(-1), beforeSleep - GRACE);
      elapse(world, MINUTE);
      run.flow.tick();
      assert.equal(run.restarts.length, 1);
      assert.equal(run.gate.cursorValue, world.now);
      await run.stop();
    }
  });

  it("moves the cursor only up to a tick seen while connected, and never when stopping", async () => {
    const world: World = { now: NOW, mono: 0, stored: [] };
    const run = await startRun(world);
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();
    assert.equal(run.gate.cursorValue, NOW);
    elapse(world, MINUTE);
    run.flow.tick();
    const confirmed = world.now;
    assert.equal(run.gate.cursorValue, confirmed);

    // The host sleeps; right after waking hkcli reconnects with nothing new, then the Gateway
    // stops, all before the next tick.
    world.now += 8 * HOUR;
    elapse(world, 10_000);
    run.flow.connectionLost();
    run.flow.synced(0);
    await run.settle();
    assert.equal(run.gate.cursorValue, confirmed);
    await run.stop();
    assert.equal(world.state?.cursor, confirmed);
  });
});
