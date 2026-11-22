import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { describe, it } from "node:test";
import { SyncProcessTracker, type SyncProcessRecord, type SyncProcessRecordStore } from "../src/sync-process.ts";
import { isRunning, waitFor } from "./helpers.ts";

const STORE_DIR = "/state/hike/default";

function memoryStore(initial?: unknown) {
  const records = new Map<string, unknown>(initial === undefined ? [] : [["default", initial]]);
  const store: SyncProcessRecordStore = {
    lookup: async (key) => records.get(key),
    register: async (key, value: SyncProcessRecord) => {
      records.set(key, value);
    },
    delete: async (key) => records.delete(key),
  };
  return { store, records };
}

function fakeKill() {
  const kills: Array<[number, string]> = [];
  return { kills, kill: (pid: number, signal: NodeJS.Signals) => void kills.push([pid, signal]) };
}

describe("SyncProcessTracker", () => {
  it("stops the sync an earlier run left behind, whatever this run's children recorded", async () => {
    const { store, records } = memoryStore({ pid: 4242, storeDir: STORE_DIR, startedAt: 1 });
    const { kills, kill } = fakeKill();
    const tracker = await SyncProcessTracker.open({ store, key: "default", storeDir: STORE_DIR, kill });
    tracker.lockHeld(5151);
    await waitFor(() => (records.get("default") as SyncProcessRecord).pid === 5151);
    assert.equal(tracker.reclaim(4242), true);
    assert.deepEqual(kills, [[4242, "SIGKILL"]]);
    assert.equal(tracker.reclaim(4242), false);
  });

  it("never stops a process it did not start for this store", async () => {
    const { kills, kill } = fakeKill();
    const open = async (record: unknown) =>
      await SyncProcessTracker.open({ store: memoryStore(record).store, key: "default", storeDir: STORE_DIR, kill });
    const record = { pid: 4242, storeDir: STORE_DIR, startedAt: 1 };
    assert.equal((await open(record)).reclaim(undefined), false);
    assert.equal((await open(record)).reclaim(4343), false);
    assert.equal((await open({ ...record, storeDir: "/other" })).reclaim(4242), false);
    assert.equal((await open({ ...record, pid: process.pid })).reclaim(process.pid), false);
    assert.equal((await open(undefined)).reclaim(4242), false);
    assert.equal((await open({ pid: "4242", storeDir: STORE_DIR })).reclaim(4242), false);
    assert.deepEqual(kills, []);
    const gone = await SyncProcessTracker.open({
      store: memoryStore(record).store,
      key: "default",
      storeDir: STORE_DIR,
      kill: () => {
        throw Object.assign(new Error("no such process"), { code: "ESRCH" });
      },
    });
    assert.equal(gone.reclaim(4242), false);
  });

  it("records a child once it holds the lock and clears only its own record", async () => {
    const { store, records } = memoryStore({ pid: 4242, storeDir: STORE_DIR, startedAt: 1 });
    const idle = await SyncProcessTracker.open({ store, key: "default", storeDir: STORE_DIR, now: () => 7 });
    idle.lockHeld(undefined);
    await idle.release();
    assert.equal((records.get("default") as SyncProcessRecord).pid, 4242);

    const tracker = await SyncProcessTracker.open({ store, key: "default", storeDir: STORE_DIR, now: () => 7 });
    tracker.lockHeld(5151);
    tracker.lockHeld(5151);
    await waitFor(() => (records.get("default") as SyncProcessRecord).pid === 5151);
    assert.deepEqual(records.get("default"), { pid: 5151, storeDir: STORE_DIR, startedAt: 7 });
    await tracker.release();
    assert.equal(records.has("default"), false);
  });

  it("really stops a leftover process", async () => {
    const orphan = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" });
    await once(orphan, "spawn");
    try {
      const { store } = memoryStore({ pid: orphan.pid, storeDir: STORE_DIR, startedAt: 1 });
      const tracker = await SyncProcessTracker.open({ store, key: "default", storeDir: STORE_DIR });
      assert.equal(tracker.reclaim(orphan.pid), true);
      await waitFor(() => !isRunning(orphan.pid));
    } finally {
      orphan.kill("SIGKILL");
    }
  });
});
