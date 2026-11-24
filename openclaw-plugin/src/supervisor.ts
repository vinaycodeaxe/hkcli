// Keeps one long-running child (hkcli sync) alive: restarts it with backoff, stops it on abort
// (graceful signal, then kill after a grace period) and never leaves it running behind us.
import type { ChildProcess } from "node:child_process";
import { NdjsonDecoder } from "./hkcli.ts";

export type SupervisedExit = {
  code: number | null;
  signal: NodeJS.Signals | null;
  runMs: number;
  spawnError?: NodeJS.ErrnoException;
  stderrTail: string;
};

export type ExitDecision =
  | { action: "restart"; delayMs?: number }
  | { action: "stop" }
  | { action: "fail"; error: Error };

export type BackoffPolicy = { initialMs: number; maxMs: number; factor: number; jitter: number };

export type SupervisorOptions = {
  spawn: () => ChildProcess;
  onValue: (value: unknown) => void;
  onMalformed?: (bytes: number) => void;
  /** Errors thrown by onValue; they never reach the child's streams. */
  onHandlerError?: (error: unknown) => void;
  onStart?: (child: ChildProcess) => void;
  onExit: (exit: SupervisedExit) => ExitDecision | Promise<ExitDecision>;
  signal: AbortSignal;
  backoff?: BackoffPolicy;
  /** A run at least this long resets the restart budget. */
  stableRunMs?: number;
  /** Consecutive short runs allowed before giving up and letting the gateway supervisor retry. */
  maxRestarts?: number;
  stopGraceMs?: number;
  random?: () => number;
};

export const DEFAULT_BACKOFF: BackoffPolicy = { initialMs: 2_000, maxMs: 60_000, factor: 2, jitter: 0.2 };

const liveChildren = new Set<ChildProcess>();
let exitHookInstalled = false;

function hasExited(child: ChildProcess): boolean {
  return child.exitCode !== null || child.signalCode !== null;
}

function killQuietly(child: ChildProcess, signal: NodeJS.Signals): void {
  try {
    child.kill(signal);
  } catch {
    // Already gone.
  }
}

/** Last-resort cleanup if the host process exits while a child still runs. */
function track(child: ChildProcess): void {
  liveChildren.add(child);
  if (!exitHookInstalled) {
    exitHookInstalled = true;
    process.once("exit", () => {
      for (const running of liveChildren) {
        if (!hasExited(running)) {
          killQuietly(running, "SIGKILL");
        }
      }
    });
  }
}

export function liveChildCount(): number {
  return [...liveChildren].filter((child) => !hasExited(child)).length;
}

/** Children already asked to stop, with their pending SIGKILL. */
const stopping = new WeakMap<ChildProcess, ReturnType<typeof setTimeout>>();

/**
 * Stops a child: SIGINT first (hkcli treats it as Ctrl+C; Windows has no signals and kills at
 * once), then SIGKILL if it is still running after `graceMs`. Asking again changes nothing: a
 * second SIGINT would make hkcli quit at once instead of finishing the sends it took on.
 */
export function stopChild(child: ChildProcess, graceMs = 5_000): ReturnType<typeof setTimeout> | undefined {
  if (hasExited(child)) {
    return undefined;
  }
  const pending = stopping.get(child);
  if (pending !== undefined) {
    return pending;
  }
  killQuietly(child, process.platform === "win32" ? "SIGKILL" : "SIGINT");
  const escalate = setTimeout(() => {
    if (!hasExited(child)) {
      killQuietly(child, "SIGKILL");
    }
  }, graceMs);
  stopping.set(child, escalate);
  child.once("close", () => clearTimeout(escalate));
  return escalate;
}

export function backoffDelay(policy: BackoffPolicy, attempt: number, random = Math.random): number {
  const base = Math.min(policy.maxMs, policy.initialMs * policy.factor ** Math.max(0, attempt - 1));
  const spread = base * policy.jitter;
  return Math.max(0, Math.round(base - spread + random() * 2 * spread));
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
    function done() {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
  });
}

function runChild(child: ChildProcess, options: SupervisorOptions): Promise<SupervisedExit> {
  const startedAt = Date.now();
  const graceMs = options.stopGraceMs ?? 5_000;
  return new Promise((resolve) => {
    const decoder = new NdjsonDecoder((value) => {
      try {
        options.onValue(value);
      } catch (error) {
        options.onHandlerError?.(error);
      }
    }, options.onMalformed);
    let stderrTail = "";
    let spawned = false;
    let spawnError: NodeJS.ErrnoException | undefined;
    let escalate: ReturnType<typeof setTimeout> | undefined;
    let finished = false;

    const finish = (code: number | null, signal: NodeJS.Signals | null) => {
      if (finished) {
        return;
      }
      finished = true;
      clearTimeout(escalate);
      options.signal.removeEventListener("abort", onAbort);
      liveChildren.delete(child);
      decoder.end();
      resolve({ code, signal, runMs: Date.now() - startedAt, spawnError, stderrTail });
    };
    const onAbort = () => {
      escalate = stopChild(child, graceMs);
    };

    child.stdout?.on("data", (chunk: Buffer) => decoder.push(chunk));
    child.stderr?.on("data", (chunk: Buffer) => {
      stderrTail = (stderrTail + chunk.toString("utf8")).slice(-4096);
    });
    child.on("spawn", () => {
      spawned = true;
      options.onStart?.(child);
    });
    child.on("error", (error: NodeJS.ErrnoException) => {
      spawnError ??= error;
      if (!spawned) {
        finish(null, null);
      }
    });
    child.on("close", (code, signal) => finish(code, signal));
    options.signal.addEventListener("abort", onAbort, { once: true });
    if (options.signal.aborted) {
      onAbort();
    }
  });
}

async function startOnce(options: SupervisorOptions): Promise<SupervisedExit> {
  let child: ChildProcess;
  try {
    child = options.spawn();
  } catch (error) {
    return { code: null, signal: null, runMs: 0, spawnError: error as NodeJS.ErrnoException, stderrTail: "" };
  }
  track(child);
  return await runChild(child, options);
}

/** Resolves when stopped by abort or an exit decision; rejects when the child keeps failing. */
export async function superviseProcess(options: SupervisorOptions): Promise<void> {
  const policy = options.backoff ?? DEFAULT_BACKOFF;
  const stableRunMs = options.stableRunMs ?? 60_000;
  const maxRestarts = options.maxRestarts ?? 6;
  let failures = 0;
  while (!options.signal.aborted) {
    const exit = await startOnce(options);
    if (options.signal.aborted) {
      return;
    }
    const decision = await options.onExit(exit);
    if (decision.action === "stop") {
      return;
    }
    if (decision.action === "fail") {
      throw decision.error;
    }
    failures = exit.runMs >= stableRunMs ? 1 : failures + 1;
    if (failures > maxRestarts) {
      const last = exit.spawnError?.code ?? exit.code ?? exit.signal;
      throw new Error(`hkcli sync stopped ${failures} times in a row (last exit: ${String(last)})`);
    }
    await sleep(decision.delayMs ?? backoffDelay(policy, failures, options.random), options.signal);
  }
}
