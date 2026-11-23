// Where an account's inbound cursor lives (plugin state, keyed by account id), and how a logout
// makes the next link start from now.
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { PluginRuntime } from "openclaw/plugin-sdk/runtime-store";
import type { InboundCursorState, InboundCursorStore } from "./inbound-gate.ts";

export type HikeCursorStore = InboundCursorStore & { clear(): Promise<void> };

type AccountRef = { accountId: string; storeDir: string };

/** Left in the hkcli store by a logout that could not reach plugin state. */
const LOGOUT_MARKER = ".openclaw-hike-logged-out";

export function openHikeCursorStore(core: PluginRuntime, accountId: string): HikeCursorStore {
  const store = core.state.openKeyedStoreV2<InboundCursorState>({ namespace: "inbound-cursor", maxEntries: 256 });
  return {
    load: async () => await store.lookup(accountId),
    save: async (state) => {
      await store.register(accountId, state);
    },
    clear: async () => {
      await store.delete(accountId);
    },
  };
}

/**
 * After a logout: drop the cursor so whatever number is linked next starts from now. Where
 * OpenClaw gives the plugin no state access (setup-only loads), a marker in the hkcli store
 * makes the next account start drop it instead.
 */
export async function forgetHikeCursor(core: PluginRuntime | null | undefined, account: AccountRef): Promise<void> {
  if (core) {
    try {
      await openHikeCursorStore(core, account.accountId).clear();
      return;
    } catch {
      // Plugin state is not available in this process.
    }
  }
  mkdirSync(account.storeDir, { recursive: true, mode: 0o700 });
  writeFileSync(path.join(account.storeDir, LOGOUT_MARKER), `${new Date().toISOString()}\n`);
}

/** Account start: honours a logout marker by clearing the cursor. True when one was found. */
export async function applyLogoutMarker(storeDir: string, cursors: Pick<HikeCursorStore, "clear">): Promise<boolean> {
  const marker = path.join(storeDir, LOGOUT_MARKER);
  if (!existsSync(marker)) {
    return false;
  }
  await cursors.clear();
  rmSync(marker, { force: true });
  return true;
}
