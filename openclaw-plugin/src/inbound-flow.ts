// Inbound work for one account run: live hkcli events, store read-backs after every (re)connect
// that may have missed something, and the rule for when the cursor may move.
import type { CatchUpResult } from "./catch-up.ts";
import type { HikeInboundMessage } from "./events.ts";
import type { InboundGate } from "./inbound-gate.ts";

/** How often the gateway calls `tick()`. */
export const INBOUND_TICK_MS = 60_000;

/** Records the message as handled; call right before its first side effect. */
export type InboundCommit = () => Promise<boolean>;

export type InboundHandler = (message: HikeInboundMessage, commit: InboundCommit) => Promise<void>;

export type InboundFlowOptions = {
  gate: InboundGate;
  /** Direct messages with ts >= `since` from hkcli's store. */
  readBack: (since: number, signal: AbortSignal) => Promise<CatchUpResult>;
  /** Runs `task` after earlier tasks with the same key finished (one chat at a time). */
  schedule: (key: string, task: () => Promise<void>) => void;
  handle: InboundHandler;
  /** Stops the running `hkcli sync` so that a fresh one starts. */
  restartSync: () => void;
  signal: AbortSignal;
  log: { info: (message: string) => void; warn: (message: string) => void; debug?: (message: string) => void };
  now?: () => number;
  /** Milliseconds from a monotonic clock (default `performance.now()`). */
  monotonicNow?: () => number;
  tickIntervalMs?: number;
};

export class InboundFlow {
  private readonly options: InboundFlowOptions;
  private readonly now: () => number;
  private readonly monotonicNow: () => number;
  private readonly tickIntervalMs: number;
  /** hkcli finished its catch-up and is receiving live events. */
  private live = false;
  /** The next sync event reads the store back: a new hkcli process, or a gap in the clock. */
  private readBackOnSync = false;
  /** Read-backs asked for and the latest one that fully finished; the cursor waits for both. */
  private requested = 0;
  private completed = 0;
  private recovery: Promise<void> | undefined;
  private lastTick: { wall: number; mono: number };
  /** The latest tick time seen while connected; the cursor never moves past it. */
  private confirmedAt: number;

  constructor(options: InboundFlowOptions) {
    this.options = options;
    this.now = options.now ?? Date.now;
    this.monotonicNow = options.monotonicNow ?? (() => performance.now());
    this.tickIntervalMs = options.tickIntervalMs ?? INBOUND_TICK_MS;
    this.lastTick = { wall: this.now(), mono: this.monotonicNow() };
    this.confirmedAt = options.gate.cursorValue;
  }

  get caughtUp(): boolean {
    return this.recovery === undefined && this.completed === this.requested;
  }

  /** A new `hkcli sync` process started; its first sync event triggers a read-back. */
  childStarted(): void {
    this.readBackOnSync = true;
    this.live = false;
  }

  /** The relay connection dropped or the hkcli process exited. */
  connectionLost(): void {
    this.live = false;
  }

  /**
   * hkcli finished a catch-up. `fetched` counts only messages it had to download, so a fresh
   * process always reads the store back too: the last run may have stored messages it never
   * got to handle.
   */
  synced(fetched: number): void {
    this.live = true;
    if (this.readBackOnSync || fetched > 0) {
      this.readBackOnSync = false;
      this.requested += 1;
    }
    this.step();
  }

  received(message: HikeInboundMessage): void {
    this.admit(message);
  }

  /**
   * Called every `tickIntervalMs`: moves the cursor while live and caught up, or retries a failed
   * read-back. A tick that comes far too late by either clock means the host slept (or the process
   * stalled), and hkcli may not have noticed yet that its connection died meanwhile: then the
   * cursor stays put and a fresh hkcli catches up and reads the store back from it.
   */
  tick(): void {
    if (this.options.signal.aborted) {
      return;
    }
    const wall = this.now();
    const mono = this.monotonicNow();
    const late = Math.max(wall - this.lastTick.wall, mono - this.lastTick.mono) > 2 * this.tickIntervalMs;
    this.lastTick = { wall, mono };
    if (late) {
      this.live = false;
      this.readBackOnSync = true;
      this.options.log.info("hike: the clock jumped (did the host sleep?); restarting hkcli sync to catch up");
      this.options.restartSync();
      return;
    }
    if (this.live) {
      this.confirmedAt = Math.max(this.confirmedAt, wall);
    }
    this.step();
  }

  /** After the abort: waits for a read-back in flight and saves what was already recorded. */
  async stop(): Promise<void> {
    await this.recovery;
    await this.options.gate.flush();
  }

  private step(): void {
    if (this.options.signal.aborted || this.recovery || !this.live) {
      return;
    }
    if (this.completed !== this.requested) {
      this.startRecovery();
      return;
    }
    this.advance(this.now());
  }

  private advance(time: number): void {
    this.options.gate.advance(Math.min(time, this.confirmedAt));
  }

  private startRecovery(): void {
    const { gate, signal } = this.options;
    const generation = this.requested;
    const startedAt = this.now();
    const since = gate.beginRecovery();
    this.recovery = this.readBack(since).then((ok) => {
      gate.endRecovery(since);
      this.recovery = undefined;
      if (!ok || signal.aborted) {
        return;
      }
      this.completed = generation;
      if (this.completed === this.requested) {
        // Everything stored before the read-back started was offered to the gate.
        this.advance(startedAt);
      }
      this.step();
    });
  }

  private async readBack(since: number): Promise<boolean> {
    const { readBack, signal, log } = this.options;
    try {
      const result = await readBack(since, signal);
      if (signal.aborted) {
        return false;
      }
      if (result.truncated) {
        log.warn("hike: too many direct messages arrived while sync was down; only the newest were read back");
      }
      for (const message of result.messages) {
        this.admit(message, since);
      }
      return true;
    } catch (error) {
      if (!signal.aborted) {
        log.warn(`hike: could not read back messages that arrived while sync was down (will retry): ${String(error)}`);
      }
      return false;
    }
  }

  private admit(message: HikeInboundMessage, bound?: number): void {
    const { gate, schedule, handle, signal, log } = this.options;
    const decision = gate.check(message, bound);
    if (decision !== "accept") {
      if (decision === "stale") {
        log.debug?.(`hike: skipped message ${message.id} from before the inbound cursor`);
      } else if (decision === "future") {
        log.debug?.(`hike: skipped message ${message.id} dated in the future`);
      }
      return;
    }
    schedule(`${message.chatType}:${message.chatId}`, async () => {
      const commit = () => gate.commit(message.id);
      try {
        await handle(message, commit);
      } finally {
        // Dropped by policy (or failed) without a side effect: still handled, never retried.
        if (!signal.aborted) {
          await commit();
        }
      }
    });
  }
}
