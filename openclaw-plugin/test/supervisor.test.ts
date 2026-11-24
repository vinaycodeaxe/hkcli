import assert from "node:assert/strict";
import type { ChildProcess } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, it } from "node:test";
import { hkcliArgs, spawnHkcli } from "../src/hkcli.ts";
import { parseSyncEvent, type HikeSyncEvent } from "../src/events.ts";
import { backoffDelay, liveChildCount, stopChild, superviseProcess, type SupervisedExit } from "../src/supervisor.ts";
import { fakeEnv, fakeHkcli, isRunning, missingHkcli, scratchDir, waitFor } from "./helpers.ts";

const fastBackoff = { initialMs: 10, maxMs: 40, factor: 2, jitter: 0 };

function syncChild(mode: string, log?: string) {
  return () =>
    spawnHkcli(fakeHkcli, hkcliArgs.sync("store"), {
      env: fakeEnv({ FAKE_HKCLI_SYNC: mode, ...(log ? { FAKE_HKCLI_LOG: log } : {}) }),
    });
}

describe("superviseProcess", () => {
  it("streams events and stops the child cleanly on abort", async () => {
    const abort = new AbortController();
    const events: HikeSyncEvent[] = [];
    const malformed: number[] = [];
    const pids: Array<number | undefined> = [];
    const exits: SupervisedExit[] = [];
    const done = superviseProcess({
      spawn: syncChild("events"),
      onStart: (child) => pids.push(child.pid),
      onValue: (value) => {
        const event = parseSyncEvent(value);
        if (event) {
          events.push(event);
        }
      },
      onMalformed: (bytes) => malformed.push(bytes),
      onExit: (exit) => {
        exits.push(exit);
        return { action: "restart" };
      },
      signal: abort.signal,
      backoff: fastBackoff,
    });
    await waitFor(() => events.some((event) => event.type === "message") && malformed.length === 1);
    assert.deepEqual(
      events.map((event) => event.type),
      ["connected", "sync", "message"],
    );
    const message = events[2];
    assert.equal(message?.type === "message" && message.message.text, "héllo 👋");
    assert.equal(liveChildCount(), 1);
    abort.abort();
    await done;
    assert.equal(exits.length, 0);
    assert.equal(liveChildCount(), 0);
    assert.equal(isRunning(pids[0]), false);
    if (process.platform !== "win32") {
      assert.equal(events.at(-1)?.type, "stopped");
    }
  });

  it("kills a child that ignores the graceful stop signal", async () => {
    const abort = new AbortController();
    let pid: number | undefined;
    let connected = false;
    const done = superviseProcess({
      spawn: syncChild("ignore-sigint"),
      onStart: (child) => {
        pid = child.pid;
      },
      onValue: (value) => {
        connected ||= parseSyncEvent(value)?.type === "sync";
      },
      onExit: () => ({ action: "restart" }),
      signal: abort.signal,
      stopGraceMs: 200,
    });
    await waitFor(() => connected);
    const started = Date.now();
    abort.abort();
    await done;
    assert.equal(isRunning(pid), false);
    assert.ok(Date.now() - started < 5_000);
  });

  it("asks a child to stop only once", { skip: process.platform === "win32" && "Windows has no SIGINT" }, async () => {
    // A second SIGINT makes hkcli quit at once, without finishing the sends it took on.
    const { dir, cleanup } = scratchDir();
    try {
      const log = path.join(dir, "log.jsonl");
      const sigints = () =>
        existsSync(log) ? readFileSync(log, "utf8").split("\n").filter((line) => line.includes("sigint-ignored")).length : 0;
      const abort = new AbortController();
      let child: ChildProcess | undefined;
      let ready = false;
      const done = superviseProcess({
        spawn: syncChild("ignore-sigint", log),
        onStart: (started) => {
          child = started;
        },
        onValue: (value) => {
          ready ||= parseSyncEvent(value)?.type === "message"; // its SIGINT handler is installed by then
        },
        onExit: () => ({ action: "stop" }),
        signal: abort.signal,
        stopGraceMs: 500,
      });
      await waitFor(() => ready);
      assert.ok(child);
      stopChild(child, 500); // a sync restart...
      await waitFor(() => sigints() === 1);
      abort.abort(); // ...then the account stopping
      await done;
      assert.equal(sigints(), 1);
    } finally {
      cleanup();
    }
  });

  it("restarts a crashing child with backoff, then gives up", async () => {
    const { dir, cleanup } = scratchDir();
    try {
      const exits: SupervisedExit[] = [];
      const error = await superviseProcess({
        spawn: syncChild("crash", path.join(dir, "log.jsonl")),
        onValue: () => {},
        onExit: (exit) => {
          exits.push(exit);
          return { action: "restart" };
        },
        signal: new AbortController().signal,
        backoff: fastBackoff,
        maxRestarts: 3,
      }).catch((caught: unknown) => caught);
      assert.ok(error instanceof Error);
      assert.match(error.message, /stopped 4 times in a row/);
      assert.deepEqual(
        exits.map((exit) => exit.code),
        [1, 1, 1, 1],
      );
      assert.equal(liveChildCount(), 0);
    } finally {
      cleanup();
    }
  });

  it("stops without restarting when told to (for example on auth_required)", async () => {
    const events: HikeSyncEvent[] = [];
    const exits: SupervisedExit[] = [];
    await superviseProcess({
      spawn: syncChild("auth"),
      onValue: (value) => {
        const event = parseSyncEvent(value);
        if (event) {
          events.push(event);
        }
      },
      onExit: (exit) => {
        exits.push(exit);
        return { action: "stop" };
      },
      signal: new AbortController().signal,
    });
    assert.deepEqual(events, [
      { type: "error", code: "auth_required", message: "the saved login no longer works; run: hkcli auth" },
    ]);
    assert.equal(exits[0]?.code, 3);
  });

  it("reports a missing binary as a spawn error", async () => {
    const exits: SupervisedExit[] = [];
    await superviseProcess({
      spawn: () => spawnHkcli(missingHkcli, hkcliArgs.sync("store")),
      onValue: () => {},
      onExit: (exit) => {
        exits.push(exit);
        return { action: "stop" };
      },
      signal: new AbortController().signal,
    });
    assert.equal(exits[0]?.spawnError?.code, "ENOENT");
  });

  it("does not let handler errors escape into the child's streams", async () => {
    const abort = new AbortController();
    const errors: unknown[] = [];
    const done = superviseProcess({
      spawn: syncChild("events"),
      onValue: () => {
        throw new Error("handler bug");
      },
      onHandlerError: (error) => errors.push(error),
      onExit: () => ({ action: "restart" }),
      signal: abort.signal,
    });
    await waitFor(() => errors.length >= 3);
    abort.abort();
    await done;
    assert.equal(liveChildCount(), 0);
  });
});

describe("backoffDelay", () => {
  it("grows exponentially up to the cap with jitter", () => {
    const policy = { initialMs: 1_000, maxMs: 8_000, factor: 2, jitter: 0.5 };
    assert.equal(backoffDelay(policy, 1, () => 0.5), 1_000);
    assert.equal(backoffDelay(policy, 3, () => 0.5), 4_000);
    assert.equal(backoffDelay(policy, 10, () => 0.5), 8_000);
    assert.equal(backoffDelay(policy, 1, () => 0), 500);
    assert.equal(backoffDelay(policy, 1, () => 1), 1_500);
  });
});
