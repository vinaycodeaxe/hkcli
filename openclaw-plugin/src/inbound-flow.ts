// Inbound work for one account run: live hkcli events, store read-backs after every (re)connect
// that may have missed something, and the rule for when the cursor may move.
import type { CatchUpResult } from "./catch-up.ts";
import type { HikeInboundMessage } from "./events.ts";
import type { InboundGate } from "./inbound-gate.ts";

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
  signal: AbortSignal;
  log: { warn: (message: string) => void; debug?: (message: string) => void };
  now?: () => number;
};

export class InboundFlow {
  private readonly options: InboundFlowOptions;
  private readonly now: () => number;
  /** hkcli finished its catch-up and is receiving live events. */
  private live = false;
  private freshChild = false;
  /** Read-backs asked for and the latest one that fully finished; the cursor waits for both. */
  private requested = 0;
  private completed = 0;
  private recovery: Promise<void> | undefined;

  constructor(options: InboundFlowOptions) {
    this.options = options;
    this.now = options.now ?? Date.now;
  }

  get caughtUp(): boolean {
    return this.recovery === undefined && this.completed === this.requested;
  }

  /** A new `hkcli sync` process started; its first sync event triggers a read-back. */
  childStarted(): void {
    this.freshChild = true;
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
    if (this.freshChild || fetched > 0) {
      this.freshChild = false;
      this.requested += 1;
    }
    this.step();
  }

  received(message: HikeInboundMessage): void {
    this.admit(message);
  }

  /** Periodic: moves the cursor while live and caught up, or retries a failed read-back. */
  tick(): void {
    this.step();
  }

  /** After the abort: waits for a read-back in flight and saves the cursor if it may move. */
  async stop(): Promise<void> {
    await this.recovery;
    if (this.live && this.caughtUp) {
      this.options.gate.advance(this.now());
    }
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
    this.options.gate.advance(this.now());
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
        gate.advance(startedAt);
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
