import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { CatchUpResult } from "../src/catch-up.ts";
import type { HikeInboundMessage } from "../src/events.ts";
import { InboundFlow, type InboundCommit } from "../src/inbound-flow.ts";
import { InboundGate, type InboundCursorState } from "../src/inbound-gate.ts";

const GRACE = 5 * 60_000;
const MINUTE = 60_000;
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

/** What outlives one account run: the clock, the plugin's saved cursor and hkcli's message store. */
type World = { now: number; state?: InboundCursorState; stored: HikeInboundMessage[] };

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
    signal: abort.signal,
    log: { warn: (line) => warnings.push(line) },
    now: clock,
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
  return { gate, flow, handled, reads, warnings, settle, stop };
}

describe("InboundFlow", () => {
  it("never answers an old direct message again after a reconnect, whatever came in between", async () => {
    const world: World = { now: NOW, stored: [] };
    const run = await startRun(world);
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();

    const monday = message("monday", world.now + 1_000);
    world.stored.push(monday);
    world.now += 2_000;
    run.flow.received(monday);
    await run.settle();

    // Two days of busy group chat; the cursor moves every minute.
    for (let minute = 0; minute < 2 * 24 * 60; minute += 1) {
      world.now += MINUTE;
      run.flow.received(message(`g${minute}`, world.now, "group"));
      await run.settle();
      run.flow.tick();
    }

    // A short relay drop: hkcli fetched one missed message, and the store is read back.
    run.flow.connectionLost();
    const missed = message("missed", world.now - 30_000);
    world.stored.push(missed);
    world.now += 10_000;
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
    const world: World = { now: NOW, stored: [] };
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
    world.now += 3_000;
    first.flow.received(a);
    first.flow.received(b);
    await new Promise((resolve) => setImmediate(resolve));
    world.now += 10 * MINUTE;
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
    const world: World = { now: NOW, stored: [] };
    const run = await startRun(world, {
      readBack: async (_since, signal) =>
        await new Promise<CatchUpResult>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new Error("hkcli stopped")), { once: true });
        }),
    });
    run.flow.childStarted();
    run.flow.synced(4);
    world.now += 30 * MINUTE;
    run.flow.tick();
    await run.stop();
    assert.equal(world.state?.cursor, NOW);
    assert.deepEqual(run.warnings, []);
  });

  it("retries a failed read-back and holds the cursor until one succeeds", async () => {
    const world: World = { now: NOW, stored: [] };
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
    world.now += MINUTE;
    run.flow.tick();
    await run.settle();
    assert.equal(reads.length, 2);
    assert.equal(run.gate.cursorValue, NOW);
    assert.equal(run.warnings.length, 2);

    failing = false;
    world.now += MINUTE;
    run.flow.tick();
    await run.settle();
    assert.equal(reads.length, 3);
    assert.deepEqual(run.handled, ["late"]);
    run.flow.tick();
    assert.equal(run.gate.cursorValue, world.now);
  });

  it("reads the store back for every new hkcli process and every catch-up that fetched messages", async () => {
    const world: World = { now: NOW, stored: [] };
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
    world.now += MINUTE;
    run.flow.synced(1);
    await run.settle();
    assert.equal(reads.length, 2);
    release({ messages: [], truncated: true });
    await run.settle();
    assert.equal(reads.length, 3);
    assert.equal(run.gate.cursorValue, world.now);
    assert.equal(run.warnings.length, 1);

    run.flow.connectionLost();
    run.flow.childStarted();
    run.flow.synced(0);
    await run.settle();
    assert.equal(reads.length, 4);
  });
});
