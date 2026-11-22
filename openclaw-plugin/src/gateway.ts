// Gateway lifecycle for one Hike account: supervise `hkcli sync`, feed its message events (and
// direct messages caught up after a reconnect) through the inbound gate and handler.
import type { ChannelGatewayContext } from "openclaw/plugin-sdk/channel-contract";
import { createAccountStatusSink, createChannelRunQueue } from "openclaw/plugin-sdk/channel-outbound";
import { channelReadyPatch } from "openclaw/plugin-sdk/gateway-runtime";
import type { PluginRuntime } from "openclaw/plugin-sdk/runtime-store";
import { HIKE_CHANNEL_ID, hkcliCommandFor, isHikeAccountLinked, type ResolvedHikeAccount } from "./accounts.ts";
import { loginCommand, logoutHikeAccount } from "./auth.ts";
import { collectMissedDirectMessages, createHkcliCatchUpSource } from "./catch-up.ts";
import { parseSyncEvent, type HikeInboundMessage } from "./events.ts";
import { HKCLI_INSTALL_HINT, hkcliArgs, probeHkcliVersion, spawnHkcli } from "./hkcli.ts";
import { InboundGate, type InboundCursorState, type InboundCursorStore } from "./inbound-gate.ts";
import { createHikeInboundHandler, type HikeLog } from "./inbound.ts";
import { getHikeRuntime } from "./runtime.ts";
import { superviseProcess, type SupervisedExit } from "./supervisor.ts";

type ActiveAccount = { abort: AbortController; done: Promise<void> };
type SyncProcessRecord = { pid: number; storeDir: string; startedAt: number };

const activeAccounts = new Map<string, ActiveAccount>();
const LIVE_CURSOR_INTERVAL_MS = 60_000;

function createLog(ctx: ChannelGatewayContext<ResolvedHikeAccount>, core: PluginRuntime): HikeLog {
  if (ctx.log) {
    return ctx.log;
  }
  const logger = core.logging.getChildLogger({ channel: HIKE_CHANNEL_ID, accountId: ctx.accountId });
  return {
    info: (message) => logger.info(message),
    warn: (message) => logger.warn(message),
    error: (message) => logger.error(message),
    debug: (message) => logger.debug?.(message),
  };
}

function cursorStore(core: PluginRuntime, accountId: string): InboundCursorStore {
  const store = core.state.openKeyedStoreV2<InboundCursorState>({ namespace: "inbound-cursor", maxEntries: 256 });
  return {
    load: async () => await store.lookup(accountId),
    save: async (state) => {
      await store.register(accountId, state);
    },
  };
}

function syncProcessStore(core: PluginRuntime) {
  return core.state.openKeyedStoreV2<SyncProcessRecord>({ namespace: "sync-process", maxEntries: 256 });
}

function lockHolderPid(message: string | undefined): number | undefined {
  const match = /\(pid (\d+)\)/.exec(message ?? "");
  return match ? Number(match[1]) : undefined;
}

function describeExit(exit: SupervisedExit): string {
  if (exit.signal) {
    return `signal ${exit.signal}`;
  }
  return `exit code ${String(exit.code)}`;
}

/**
 * A previous gateway that died without stopping its child leaves `hkcli sync` holding the
 * store lock. Kill it only when the lock holder is the process this plugin itself started.
 */
async function reclaimOrphan(
  core: PluginRuntime,
  account: ResolvedHikeAccount,
  holderPid: number | undefined,
  log: HikeLog,
): Promise<boolean> {
  if (!holderPid) {
    return false;
  }
  const record = await syncProcessStore(core).lookup(account.accountId).catch(() => undefined);
  if (!record || record.pid !== holderPid || record.storeDir !== account.storeDir) {
    return false;
  }
  try {
    process.kill(holderPid, "SIGKILL");
    log.warn(`hike: stopped leftover hkcli sync (pid ${holderPid}) from an earlier gateway run`);
    return true;
  } catch {
    return false;
  }
}

export async function startHikeAccount(ctx: ChannelGatewayContext<ResolvedHikeAccount>): Promise<void> {
  const { account } = ctx;
  const accountId = account.accountId;
  const core = getHikeRuntime();
  const log = createLog(ctx, core);
  const status = createAccountStatusSink({ accountId, setStatus: ctx.setStatus });
  const command = hkcliCommandFor(account);
  const blocked = (message: string) => {
    status({ connected: false, lifecycle: "blocked", terminalDisconnect: true, lastError: message });
    log.error(`hike: ${message}`);
  };

  status({ connected: false, lifecycle: "starting" });
  const version = await probeHkcliVersion(command, { timeoutMs: 15_000 });
  if (!version.ok) {
    blocked(version.message);
    return;
  }
  if (!isHikeAccountLinked(account)) {
    blocked(`Hike account "${accountId}" is not linked. Run: ${loginCommand(accountId)}`);
    return;
  }

  const abort = new AbortController();
  const gate = await InboundGate.open({
    store: cursorStore(core, accountId),
    onError: (error) => log.warn(`hike: could not save the inbound cursor: ${String(error)}`),
  });
  const handle = createHikeInboundHandler({
    cfg: ctx.cfg,
    account,
    core,
    log,
    signal: abort.signal,
    onInbound: (at) => status({ lastInboundAt: at }),
    onOutbound: (at) => status({ lastOutboundAt: at }),
  });
  const queue = createChannelRunQueue({
    setStatus: (patch) => status(patch),
    abortSignal: abort.signal,
    onError: (error) => log.error(`hike: inbound message failed: ${String(error)}`),
  });

  const enqueue = (message: HikeInboundMessage) => {
    const decision = gate.check(message);
    if (decision !== "accept") {
      if (decision === "stale") {
        log.debug?.(`hike: skipped message ${message.id} from before the inbound cursor`);
      }
      return;
    }
    queue.enqueue(`${message.chatType}:${message.chatId}`, async () => {
      // Marked handled right before its first side effect (pairing reply or agent dispatch), so a
      // crash mid-reply never answers twice; a stop before that leaves it for the next run.
      const commit = () => gate.complete(message.id);
      try {
        await handle(message, commit);
      } finally {
        if (!abort.signal.aborted) {
          commit();
        }
      }
    });
  };

  let live = false;
  let catchUps = 0;
  let catchUp: Promise<void> = Promise.resolve();
  const runCatchUp = (syncedAt: number) => {
    catchUps += 1;
    catchUp = catchUp
      .then(async () => {
        const source = createHkcliCatchUpSource(command, account.storeDir, { signal: abort.signal });
        for (const message of await collectMissedDirectMessages(source, gate.threshold)) {
          enqueue(message);
        }
        gate.advance(syncedAt);
      })
      .catch((error: unknown) => {
        if (!abort.signal.aborted) {
          log.warn(`hike: could not read messages that arrived while sync was down: ${String(error)}`);
        }
      })
      .finally(() => {
        catchUps -= 1;
      });
  };
  const tick = setInterval(() => {
    if (live && catchUps === 0) {
      gate.advance(Date.now());
    }
  }, LIVE_CURSOR_INTERVAL_MS);
  tick.unref?.();

  const stopOnAbort = () => abort.abort();
  ctx.abortSignal.addEventListener("abort", stopOnAbort, { once: true });
  if (ctx.abortSignal.aborted) {
    abort.abort();
  }
  let finished: () => void = () => {};
  const done = new Promise<void>((resolve) => {
    finished = resolve;
  });
  activeAccounts.get(accountId)?.abort.abort();
  activeAccounts.set(accountId, { abort, done });

  let lastError: { code: string; message: string } | undefined;
  try {
    await superviseProcess({
      signal: abort.signal,
      spawn: () => spawnHkcli(command, hkcliArgs.sync(account.storeDir)),
      onStart: (child) => {
        lastError = undefined;
        if (child.pid) {
          const record: SyncProcessRecord = { pid: child.pid, storeDir: account.storeDir, startedAt: Date.now() };
          void syncProcessStore(core)
            .register(accountId, record)
            .catch(() => undefined);
        }
        log.info(`hike: hkcli ${version.version} sync started for account "${accountId}"`);
      },
      onValue: (value) => {
        const event = parseSyncEvent(value);
        switch (event?.type) {
          case "message":
            enqueue(event.message);
            return;
          case "connected":
            status(channelReadyPatch());
            return;
          case "sync":
            live = true;
            if (event.messages > 0) {
              runCatchUp(Date.now());
            } else if (catchUps === 0) {
              gate.advance(Date.now());
            }
            return;
          case "disconnected":
            live = false;
            status({
              connected: false,
              lifecycle: "recovering",
              lastDisconnect: { at: Date.now(), ...(event.reason ? { error: event.reason } : {}) },
            });
            log.warn(`hike: connection to Hike lost (${event.reason ?? "unknown"}); hkcli is reconnecting`);
            return;
          case "error":
            lastError = { code: event.code, message: event.message };
            return;
          default:
            return;
        }
      },
      onMalformed: (bytes) => log.warn(`hike: skipped a malformed hkcli output line (${bytes} bytes)`),
      onHandlerError: (error) => log.error(`hike: event handling failed: ${String(error)}`),
      onExit: async (exit) => {
        live = false;
        if (exit.spawnError?.code === "ENOENT") {
          blocked(`hkcli was not found (${account.cliPath}). ${HKCLI_INSTALL_HINT}`);
          return { action: "stop" };
        }
        if (exit.code === 3 || lastError?.code === "auth_required") {
          blocked(
            `Hike login no longer works (${lastError?.message ?? "auth required"}). Run: ${loginCommand(accountId)}`,
          );
          return { action: "stop" };
        }
        if (lastError?.code === "store_locked") {
          const reclaimed = await reclaimOrphan(core, account, lockHolderPid(lastError.message), log);
          const message = `${lastError.message}. Stop the other hkcli sync on ${account.storeDir}.`;
          status({
            connected: false,
            lifecycle: "recovering",
            lastDisconnect: { at: Date.now(), error: message },
            lastError: message,
          });
          return { action: "restart", delayMs: reclaimed ? 1_000 : 15_000 };
        }
        const reason = lastError ? `${lastError.code}: ${lastError.message}` : describeExit(exit);
        status({
          connected: false,
          lifecycle: "recovering",
          lastDisconnect: { at: Date.now(), error: `hkcli sync stopped (${reason})` },
          lastError: `hkcli sync stopped (${reason})`,
        });
        log.warn(`hike: hkcli sync stopped (${reason}); restarting`);
        return { action: "restart" };
      },
    });
  } finally {
    clearInterval(tick);
    ctx.abortSignal.removeEventListener("abort", stopOnAbort);
    abort.abort();
    await catchUp;
    if (live) {
      gate.advance(Date.now());
    }
    await gate.flush();
    await syncProcessStore(core)
      .delete(accountId)
      .catch(() => undefined);
    if (activeAccounts.get(accountId)?.abort === abort) {
      activeAccounts.delete(accountId);
    }
    finished();
  }
}

/** Gateway logout: make sure this account's sync is stopped, then log hkcli out. */
export async function logoutHikeGatewayAccount(account: ResolvedHikeAccount) {
  const active = activeAccounts.get(account.accountId);
  if (active) {
    active.abort.abort();
    await active.done;
  }
  return await logoutHikeAccount(account);
}
