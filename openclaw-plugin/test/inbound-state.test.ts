import assert from "node:assert/strict";
import { readdirSync } from "node:fs";
import { describe, it } from "node:test";
import type { PluginRuntime } from "openclaw/plugin-sdk/runtime-store";
import { applyLogoutMarker, forgetHikeCursor } from "../src/inbound-state.ts";
import { scratchDir } from "./helpers.ts";

function fakeRuntime(deleted: string[]): PluginRuntime {
  const store = {
    lookup: async () => undefined,
    register: async () => {},
    delete: async (key: string) => {
      deleted.push(key);
      return true;
    },
  };
  return { state: { openKeyedStoreV2: () => store } } as unknown as PluginRuntime;
}

/** What setup-only loads hand a plugin: every runtime property throws. */
const unavailableRuntime = new Proxy(
  {},
  {
    get() {
      throw new Error("runtime is intentionally unavailable");
    },
  },
) as unknown as PluginRuntime;

describe("forgetting the inbound cursor on logout", () => {
  it("deletes the account's cursor from plugin state", async () => {
    const scratch = scratchDir();
    try {
      const deleted: string[] = [];
      await forgetHikeCursor(fakeRuntime(deleted), { accountId: "work", storeDir: scratch.dir });
      assert.deepEqual(deleted, ["work"]);
      assert.deepEqual(readdirSync(scratch.dir), []);
    } finally {
      scratch.cleanup();
    }
  });

  it("leaves a marker the next account start honours when plugin state is out of reach", async () => {
    for (const runtime of [null, unavailableRuntime]) {
      const scratch = scratchDir();
      try {
        await forgetHikeCursor(runtime, { accountId: "default", storeDir: scratch.dir });
        assert.equal(readdirSync(scratch.dir).length, 1);

        const deleted: string[] = [];
        const cursors = { clear: async () => void deleted.push("cleared") };
        assert.equal(await applyLogoutMarker(scratch.dir, cursors), true);
        assert.deepEqual(deleted, ["cleared"]);
        assert.deepEqual(readdirSync(scratch.dir), []);
        assert.equal(await applyLogoutMarker(scratch.dir, cursors), false);
        assert.deepEqual(deleted, ["cleared"]);
      } finally {
        scratch.cleanup();
      }
    }
  });

  it("keeps the marker when clearing the cursor fails, so the next start tries again", async () => {
    const scratch = scratchDir();
    try {
      await forgetHikeCursor(null, { accountId: "default", storeDir: scratch.dir });
      const failing = {
        clear: async () => {
          throw new Error("plugin state is closed");
        },
      };
      await assert.rejects(applyLogoutMarker(scratch.dir, failing), /plugin state is closed/);
      assert.equal(readdirSync(scratch.dir).length, 1);
    } finally {
      scratch.cleanup();
    }
  });
});
