// Needs the openclaw package (installed in CI): drives startHikeAccount with a fake plugin runtime
// and the fake hkcli.
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { after, before, describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { resolveHikeAccount, type ResolvedHikeAccount } from "../../src/accounts.ts";
import { startHikeAccount } from "../../src/gateway.ts";
import { clearHikeRuntime, setHikeRuntime } from "../../src/runtime.ts";
import { scratchDir, waitFor } from "../helpers.ts";

const here = path.dirname(fileURLToPath(import.meta.url));

/** In-memory plugin state that records every operation, in order. */
function fakeState() {
  const namespaces = new Map<string, Map<string, unknown>>();
  const ops: string[] = [];
  let cursorSaves = 0;
  let failingCursorSave = 0;
  return {
    ops,
    failCursorSave(number: number) {
      failingCursorSave = number;
    },
    value(namespace: string, key: string): Record<string, unknown> | undefined {
      return namespaces.get(namespace)?.get(key) as Record<string, unknown> | undefined;
    },
    openKeyedStoreV2({ namespace }: { namespace: string }) {
      const values = namespaces.get(namespace) ?? new Map<string, unknown>();
      namespaces.set(namespace, values);
      return {
        lookup: async (key: string) => {
          ops.push(`lookup ${namespace}`);
          return structuredClone(values.get(key));
        },
        register: async (key: string, value: Record<string, unknown>) => {
          const label = namespace === "inbound-cursor" ? `${namespace} ${String(value.user)}` : namespace;
          if (namespace === "inbound-cursor" && ++cursorSaves === failingCursorSave) {
            ops.push(`failed save ${label}`);
            throw new Error("plugin state is busy");
          }
          ops.push(`save ${label}`);
          values.set(key, structuredClone(value));
        },
        delete: async (key: string) => {
          ops.push(`delete ${namespace}`);
          return values.delete(key);
        },
      };
    },
  };
}

/** Just enough runtime for the inbound handler: every DM is from an unknown sender. */
function fakeRuntime(state: ReturnType<typeof fakeState>, upsertPairingRequest: () => Promise<unknown>) {
  return {
    config: { current: () => ({ channels: { hike: {} } }) },
    state: { openKeyedStoreV2: (options: { namespace: string }) => state.openKeyedStoreV2(options) },
    channel: {
      activity: { record: () => {} },
      commands: { shouldHandleTextCommands: () => false },
      text: { hasControlCommand: () => false },
      inbound: {
        ingress: {
          createResolver: () => ({
            message: async () => ({
              ingress: { admission: "pairing-required" },
              routeAccess: {},
              commandAccess: { authorized: false },
            }),
          }),
        },
      },
      pairing: { upsertPairingRequest, readAllowFromStore: async () => [] },
    },
  };
}

function gatewayContext(
  account: ResolvedHikeAccount,
  abort: AbortController,
  logs: string[],
  statuses: Array<Record<string, unknown>> = [],
) {
  const log = (line: string) => void logs.push(line);
  return {
    cfg: { channels: { hike: { cliPath: account.cliPath, storeDir: account.storeDir } } },
    accountId: account.accountId,
    account,
    runtime: { log: () => {}, error: () => {}, exit: () => {} },
    abortSignal: abort.signal,
    log: { info: log, warn: log, error: log, debug: () => {} },
    getStatus: () => ({ accountId: account.accountId }),
    setStatus: (snapshot: Record<string, unknown>) => void statuses.push(snapshot),
  } as never;
}

describe("account start", () => {
  let scratch: ReturnType<typeof scratchDir>;
  let account: ResolvedHikeAccount;
  before(() => {
    scratch = scratchDir();
    const wrapper = path.join(scratch.dir, "hkcli");
    const fake = path.join(here, "..", "fixtures", "fake-hkcli.mjs");
    writeFileSync(wrapper, `#!/bin/sh\nexec "${process.execPath}" "${fake}" "$@"\n`);
    chmodSync(wrapper, 0o755);
    const storeDir = path.join(scratch.dir, "store");
    mkdirSync(storeDir, { recursive: true });
    writeFileSync(path.join(storeDir, "session.json"), "{}");
    account = resolveHikeAccount({ cfg: { channels: { hike: { cliPath: wrapper, storeDir } } } as never });
    process.env.FAKE_HKCLI_SYNC_TS = "now";
  });
  after(() => {
    clearHikeRuntime();
    delete process.env.FAKE_HKCLI_SYNC_TS;
    delete process.env.FAKE_HKCLI_USER;
    scratch.cleanup();
  });

  it("starts a replacement run only after the run it replaces has written its last state", { timeout: 120_000 }, async () => {
    const state = fakeState();
    let answering: () => void = () => {};
    const replyStarted = new Promise<void>((resolve) => {
      answering = resolve;
    });
    let releaseReply: () => void = () => {};
    const replyHeld = new Promise<void>((resolve) => {
      releaseReply = resolve;
    });
    let pairings = 0;
    setHikeRuntime(
      fakeRuntime(state, async () => {
        pairings += 1;
        if (pairings === 1) {
          answering();
          await replyHeld;
        }
        return { code: "ABCD2345", created: true };
      }) as never,
    );
    const logs: string[] = [];
    const stopA = new AbortController();
    const stopB = new AbortController();
    try {
      // Run A, linked as Hike user 1001: an unknown sender's DM is being answered, and saving
      // that it was handled failed, so A still has state to write when it stops.
      state.failCursorSave(2);
      process.env.FAKE_HKCLI_USER = "1001";
      const runA = startHikeAccount(gatewayContext(account, stopA, logs));
      await Promise.race([
        replyStarted,
        new Promise((_resolve, reject) =>
          setTimeout(() => reject(new Error(`run A never started answering:\n${logs.join("\n")}`)), 30_000).unref(),
        ),
      ]);

      // A reload stops A with that reply in flight, and run B starts for the account, which has
      // been linked to another number meanwhile.
      stopA.abort();
      process.env.FAKE_HKCLI_USER = "2002";
      const mark = state.ops.length;
      const runB = startHikeAccount(gatewayContext(account, stopB, logs));
      await new Promise((resolve) => setTimeout(resolve, 1_000));
      assert.deepEqual(state.ops.slice(mark), [], "B touched state while A was still stopping");

      releaseReply();
      await runA;
      assert.deepEqual(state.ops.slice(mark), ["save inbound-cursor 1001", "delete sync-process"]);

      await waitFor(
        () =>
          state.value("inbound-cursor", "default")?.user === "2002" &&
          state.value("sync-process", "default") !== undefined,
        20_000,
      );
      assert.ok(logs.some((line) => /now linked to Hike user 2002 \(was 1001\)/.test(line)));
      stopB.abort();
      await runB;
      assert.equal(state.value("inbound-cursor", "default")?.user, "2002");
    } finally {
      releaseReply();
      stopA.abort();
      stopB.abort();
    }
  });

  it("does not start, or handle anything, until a new cursor is saved", { timeout: 60_000 }, async () => {
    const state = fakeState();
    state.failCursorSave(1);
    let pairings = 0;
    setHikeRuntime(
      fakeRuntime(state, async () => {
        pairings += 1;
        return { code: "ABCD2345", created: true };
      }) as never,
    );
    const calls = path.join(scratch.dir, "calls.ndjson");
    const statuses: Array<Record<string, unknown>> = [];
    process.env.FAKE_HKCLI_USER = "1001";
    process.env.FAKE_HKCLI_LOG = calls;
    try {
      await assert.rejects(
        startHikeAccount(gatewayContext(account, new AbortController(), [], statuses)),
        /could not save the new inbound cursor/,
      );
    } finally {
      delete process.env.FAKE_HKCLI_LOG;
    }
    assert.deepEqual(state.ops, ["lookup sync-process", "lookup inbound-cursor", "failed save inbound-cursor 1001"]);
    assert.equal(statuses.at(-1)?.lifecycle, "recovering");
    const commands = readFileSync(calls, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line).command);
    assert.deepEqual(commands, ["auth status"]);
    assert.equal(pairings, 0);
  });
});
