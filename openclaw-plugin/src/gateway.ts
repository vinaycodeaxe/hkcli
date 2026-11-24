// Gateway lifecycle for one Hike account: supervise `hkcli sync` and feed its events (and direct
// messages read back from its store after every reconnect) through the inbound flow.
import type { ChildProcess } from "node:child_process";
import type { ChannelGatewayContext } from "openclaw/plugin-sdk/channel-contract";
import { createAccountStatusSink, createChannelRunQueue } from "openclaw/plugin-sdk/channel-outbound";
import { channelReadyPatch } from "openclaw/plugin-sdk/gateway-runtime";
import type { PluginRuntime } from "openclaw/plugin-sdk/runtime-store";
import { AccountRuns } from "./account-runs.ts";
import { HIKE_CHANNEL_ID, hkcliCommandFor, isHikeAccountLinked, type ResolvedHikeAccount } from "./accounts.ts";
import { loginCommand, logoutHikeAccount } from "./auth.ts";
import { collectMissedDirectMessages, createHkcliCatchUpSource } from "./catch-up.ts";
import { parseSyncEvent } from "./events.ts";
import {
  HKCLI_INSTALL_HINT,
  hkcliArgs,
  probeHkcliVersion,
  readHkcliUserId,
  spawnHkcli,
  type HkcliCommand,
} from "./hkcli.ts";
import { INBOUND_TICK_MS, InboundFlow } from "./inbound-flow.ts";
import { InboundGate } from "./inbound-gate.ts";
import { applyLogoutMarker, openHikeCursorStore } from "./inbound-state.ts";
import { createHikeInboundHandler, type HikeLog } from "./inbound.ts";
import { getHikeRuntime } from "./runtime.ts";
import { stopChild, superviseProcess, type SupervisedExit } from "./supervisor.ts";
import { SyncProcessTracker, type SyncProcessRecord } from "./sync-process.ts";

const accountRuns = new AccountRuns();
/** hkcli sync finishes sends it took on for `hkcli send` before it disconnects (up to 25 s). */
const SYNC_STOP_GRACE_MS = 30_000;
/** A replaced run takes up to ~90 s to stop: hkcli (30 s), then replies in flight (60 s). */
const PREVIOUS_RUN_WAIT_MS = 120_000;

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

/**
 * The account's inbound gate, bound to the Hike user its store is logged in as. Errors are thrown
 * so the start is retried; a guessed fresh cursor would skip what arrived while it was down.
 */
async function openAccountGate(params: {
  core: PluginRuntime;
  account: ResolvedHikeAccount;
  command: HkcliCommand;
  log: HikeLog;
  signal: AbortSignal;
  isStopping: () => boolean;
}): Promise<InboundGate> {
  const { core, account, log } = params;
  const user = await readHkcliUserId(params.command, account.storeDir, { signal: params.signal });
  const cursors = openHikeCursorStore(core, account.accountId);
  if (await applyLogoutMarker(account.storeDir, cursors)) {
    log.info(`hike: account "${account.accountId}" was logged out since it last ran; answering new messages only`);
  }
  return await InboundGate.open({
    store: cursors,
    user,
    onUserChanged: (previous, current) => {
      log.info(
        `hike: account "${account.accountId}" is now linked to Hike user ${current} (was ${previous}); answering new messages only`,
      );
    },
    onError: (error) => {
      // During Gateway shutdown plugin state closes first; the last successful save stands.
      const message = `hike: could not save the inbound cursor: ${String(error)}`;
      if (params.isStopping()) {
        log.debug?.(message);
      } else {
        log.warn(message);
      }
    },
  });
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

export async function startHikeAccount(ctx: ChannelGatewayContext<ResolvedHikeAccount>): Promise<void> {
  const { account } = ctx;
  const accountId = account.accountId;
  const core = getHikeRuntime();
  const log = createLog(ctx, core);
  // Registered before this run reads or writes any state of the account, so a logout or the next
  // start finds it and waits; the run it replaces is asked to stop and waited for first.
  const run = accountRuns.begin(accountId);
  const stopOnAbort = () => run.abort.abort();
  ctx.abortSignal.addEventListener("abort", stopOnAbort, { once: true });
  if (ctx.abortSignal.aborted) {
    run.abort.abort();
  }
  try {
    if (!(await run.previousStopped(PREVIOUS_RUN_WAIT_MS))) {
      log.warn(
        `hike: the previous run of account "${accountId}" is still stopping after ${PREVIOUS_RUN_WAIT_MS / 1000}s; starting anyway`,
      );
    }
    if (run.abort.signal.aborted) {
      return;
    }
    await runHikeAccount(ctx, core, log, run.abort);
  } finally {
    ctx.abortSignal.removeEventListener("abort", stopOnAbort);
    run.finish();
  }
}

async function runHikeAccount(
  ctx: ChannelGatewayContext<ResolvedHikeAccount>,
  core: PluginRuntime,
  log: HikeLog,
  abort: AbortController,
): Promise<void> {
  const { account } = ctx;
  const accountId = account.accountId;
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

  const processes = await SyncProcessTracker.open({
    store: core.state.openKeyedStoreV2<SyncProcessRecord>({ namespace: "sync-process", maxEntries: 256 }),
    key: accountId,
    storeDir: account.storeDir,
  });
  let gate: InboundGate;
  try {
    gate = await openAccountGate({
      core,
      account,
      command,
      log,
      signal: abort.signal,
      isStopping: () => abort.signal.aborted,
    });
  } catch (error) {
    if (abort.signal.aborted) {
      return;
    }
    const reason = error instanceof Error ? error.message : String(error);
    const message = `could not open the inbound state of Hike account "${accountId}" (${reason}); retrying`;
    status({ connected: false, lifecycle: "recovering", lastError: message });
    log.error(`hike: ${message}`);
    throw error;
  }
  const queue = createChannelRunQueue({
    setStatus: (patch) => status(patch),
    abortSignal: abort.signal,
    onError: (error) => log.error(`hike: inbound message failed: ${String(error)}`),
  });
  let child: ChildProcess | undefined;
  let restartRequested = false;
  let lastError: { code: string; message: string } | undefined;
  const flow = new InboundFlow({
    gate,
    readBack: async (since, signal) =>
      await collectMissedDirectMessages(createHkcliCatchUpSource(command, account.storeDir, { signal }), since),
    schedule: (key, task) => queue.enqueue(key, task),
    handle: createHikeInboundHandler({
      readConfig: () => core.config.current({ clone: true }),
      account,
      core,
      log,
      signal: abort.signal,
      onInbound: (at) => status({ lastInboundAt: at }),
      onOutbound: (at) => status({ lastOutboundAt: at }),
    }),
    restartSync: () => {
      if (child && child.exitCode === null && child.signalCode === null) {
        restartRequested = true;
        stopChild(child, SYNC_STOP_GRACE_MS);
      }
    },
    signal: abort.signal,
    log,
  });
  const tick = setInterval(() => flow.tick(), INBOUND_TICK_MS);
  tick.unref?.();

  try {
    await superviseProcess({
      signal: abort.signal,
      stopGraceMs: SYNC_STOP_GRACE_MS,
      spawn: () => spawnHkcli(command, hkcliArgs.sync(account.storeDir)),
      onStart: (started) => {
        child = started;
        lastError = undefined;
        flow.childStarted();
        log.info(`hike: hkcli ${version.version} sync started for account "${accountId}"`);
      },
      onValue: (value) => {
        const event = parseSyncEvent(value);
        switch (event?.type) {
          case "message":
            flow.received(event.message);
            return;
          case "connected":
            // hkcli prints nothing before it holds the store lock.
            processes.lockHeld(child?.pid);
            status(channelReadyPatch());
            return;
          case "sync":
            processes.lockHeld(child?.pid);
            flow.synced(event.messages);
            return;
          case "disconnected":
            flow.connectionLost();
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
        flow.connectionLost();
        const restart = restartRequested;
        restartRequested = false;
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
        if (restart) {
          status({ connected: false, lifecycle: "recovering" });
          return { action: "restart", delayMs: 0 };
        }
        if (lastError?.code === "store_locked") {
          const holder = lockHolderPid(lastError.message);
          const reclaimed = processes.reclaim(holder);
          if (reclaimed) {
            log.warn(`hike: stopped leftover hkcli sync (pid ${holder}) from an earlier gateway run`);
          }
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
    abort.abort();
    await flow.stop();
    await processes.release();
  }
}

/**
 * Gateway logout: stop this account's run and wait until it (and any run it was still waiting
 * for) has stopped, so no reply or final save lands after the logout; then log hkcli out.
 */
export async function logoutHikeGatewayAccount(account: ResolvedHikeAccount) {
  const active = accountRuns.current(account.accountId);
  if (active) {
    active.abort.abort();
    await active.done;
  }
  return await logoutHikeAccount(account);
}
