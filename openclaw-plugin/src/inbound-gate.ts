// Decides which inbound messages are new: drops our own echoes, duplicates and history, and
// keeps the cursor that lets a restart (or a reconnect) answer what arrived while sync was down.

export type InboundCursorState = {
  v: 2;
  /** Time (epoch ms) up to which every inbound message was handled or deliberately skipped. */
  cursor: number;
  /** Read-backs never go below this; raised only when `seen` overflowed. */
  floor?: number;
  /** Handled direct messages ([id, ts]) newer than the oldest bound a read-back may still use. */
  seen: Array<[string, number]>;
};

export type InboundCursorStore = {
  load(): Promise<unknown>;
  save(state: InboundCursorState): Promise<void>;
};

export type InboundGateDecision = "accept" | "self" | "duplicate" | "stale";

export type GateMessage = { id: string; fromMe: boolean; ts: number; chatType: "direct" | "group" };

export type InboundGateOptions = {
  store: InboundCursorStore;
  /** Slack for sender clocks and delivery delays around the cursor. */
  graceMs?: number;
  /** Messages older than this are never answered, however long sync was down. */
  maxAgeMs?: number;
  /** Hard cap on remembered ids (plugin state values are limited to 64 KiB). */
  maxSeen?: number;
  saveDelayMs?: number;
  now?: () => number;
  onError?: (error: unknown) => void;
};

export const DEFAULT_CURSOR_GRACE_MS = 5 * 60_000;
export const DEFAULT_MAX_RECOVERY_AGE_MS = 12 * 60 * 60_000;
const DEFAULT_MAX_SEEN = 800;
const RECENT_GROUP_IDS = 1000;

type LoadedState = { cursor: number; floor: number; seen: Map<string, number> };

function finite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function readState(value: unknown): LoadedState | undefined {
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  const state = value as { v?: unknown; cursor?: unknown; floor?: unknown; seen?: unknown };
  if (!finite(state.cursor) || !Array.isArray(state.seen)) {
    return undefined;
  }
  const seen = new Map<string, number>();
  if (state.v === 2) {
    for (const entry of state.seen) {
      if (Array.isArray(entry) && typeof entry[0] === "string" && finite(entry[1])) {
        seen.set(entry[0], entry[1]);
      }
    }
    return { cursor: state.cursor, floor: finite(state.floor) ? state.floor : 0, seen };
  }
  if (state.v === 1) {
    // Version 1 kept bare ids; they are at most a grace period older than the cursor.
    for (const id of state.seen) {
      if (typeof id === "string") {
        seen.set(id, state.cursor);
      }
    }
    return { cursor: state.cursor, floor: 0, seen };
  }
  return undefined;
}

export class InboundGate {
  /** Lower bound for live events in this run: (cursor at start) - grace. */
  readonly liveFloor: number;
  private cursor: number;
  private recoveryFloor: number;
  private readonly seen: Map<string, number>;
  /** Group messages are never read back, so their ids are only remembered in memory. */
  private readonly recentGroupIds = new Set<string>();
  /** Accepted but not handled yet; never persisted, so the next run offers them again. */
  private readonly pending = new Map<string, { ts: number; direct: boolean }>();
  private readonly activeBounds: number[] = [];
  private readonly store: InboundCursorStore;
  private readonly graceMs: number;
  private readonly maxAgeMs: number;
  private readonly maxSeen: number;
  private readonly saveDelayMs: number;
  private readonly now: () => number;
  private readonly onError: (error: unknown) => void;
  private dirty = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private chain: Promise<boolean> = Promise.resolve(true);

  private constructor(options: InboundGateOptions, state: LoadedState | undefined) {
    this.store = options.store;
    this.now = options.now ?? Date.now;
    this.graceMs = options.graceMs ?? DEFAULT_CURSOR_GRACE_MS;
    this.maxAgeMs = options.maxAgeMs ?? DEFAULT_MAX_RECOVERY_AGE_MS;
    this.maxSeen = options.maxSeen ?? DEFAULT_MAX_SEEN;
    this.saveDelayMs = options.saveDelayMs ?? 1000;
    this.onError = options.onError ?? (() => {});
    const openedAt = this.now();
    this.cursor = state ? Math.min(state.cursor, openedAt) : openedAt;
    this.liveFloor = this.cursor - this.graceMs;
    this.recoveryFloor = state?.floor ?? 0;
    this.seen = state?.seen ?? new Map();
    this.dirty = !state;
  }

  static async open(options: InboundGateOptions): Promise<InboundGate> {
    let state: LoadedState | undefined;
    try {
      state = readState(await options.store.load());
    } catch (error) {
      options.onError?.(error);
    }
    const gate = new InboundGate(options, state);
    if (!state) {
      await gate.flush();
    }
    return gate;
  }

  get cursorValue(): number {
    return this.cursor;
  }

  /** Live events use the run's floor; read-backs pass the bound they read from. */
  check(message: GateMessage, bound: number = this.liveFloor): InboundGateDecision {
    if (message.fromMe) {
      return "self";
    }
    if (this.seen.has(message.id) || this.pending.has(message.id) || this.recentGroupIds.has(message.id)) {
      return "duplicate";
    }
    if (message.ts < bound || message.ts < this.now() - this.maxAgeMs) {
      return "stale";
    }
    const direct = message.chatType === "direct";
    this.pending.set(message.id, { ts: message.ts, direct });
    if (!direct) {
      this.recentGroupIds.add(message.id);
      if (this.recentGroupIds.size > RECENT_GROUP_IDS) {
        this.recentGroupIds.delete(this.recentGroupIds.values().next().value as string);
      }
    }
    return "accept";
  }

  /**
   * Marks an accepted message as handled and saves that before the caller acts on it, so a
   * crash or restart never answers it twice. Returns false when the save failed.
   */
  async commit(id: string): Promise<boolean> {
    const entry = this.pending.get(id);
    if (!entry) {
      return true;
    }
    this.pending.delete(id);
    // Only direct messages are ever read back from the store; group ids need no memory.
    if (!entry.direct) {
      return true;
    }
    this.seen.set(id, entry.ts);
    this.dirty = true;
    return await this.flush();
  }

  /** Everything before `time` was handled; queued messages hold the cursor back. */
  advance(time: number): void {
    let next = Math.min(time, this.now());
    for (const { ts } of this.pending.values()) {
      next = Math.min(next, ts - 1);
    }
    if (next > this.cursor) {
      this.cursor = next;
      this.scheduleSave();
    }
  }

  /** Lower bound for reading the store back now; older messages were handled or skipped. */
  beginRecovery(): number {
    const since = Math.max(this.cursor - this.graceMs, this.now() - this.maxAgeMs, this.recoveryFloor);
    this.activeBounds.push(since);
    return since;
  }

  endRecovery(since: number): void {
    const index = this.activeBounds.indexOf(since);
    if (index >= 0) {
      this.activeBounds.splice(index, 1);
    }
  }

  flush(): Promise<boolean> {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.chain = this.chain.then(async () => {
      if (!this.dirty) {
        return true;
      }
      this.dirty = false;
      const state = this.snapshot();
      try {
        await this.store.save(state);
        return true;
      } catch (error) {
        this.dirty = true;
        this.onError(error);
        return false;
      }
    });
    return this.chain;
  }

  private scheduleSave(): void {
    this.dirty = true;
    if (this.timer) {
      return;
    }
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.flush();
    }, this.saveDelayMs);
    this.timer.unref?.();
  }

  private snapshot(): InboundCursorState {
    this.prune();
    const seen = [...this.seen].sort((a, b) => a[1] - b[1] || (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
    return {
      v: 2,
      cursor: this.cursor,
      ...(this.recoveryFloor > 0 ? { floor: this.recoveryFloor } : {}),
      seen,
    };
  }

  /**
   * Forgets ids no read-back can offer again: older than every bound in use (cursor - grace,
   * running read-backs) or than the age limit. Count only matters past `maxSeen`, and then the
   * read-back floor moves up instead of letting an old id be answered again.
   */
  private prune(): void {
    const keepFrom = Math.max(
      Math.min(this.cursor - this.graceMs, ...this.activeBounds),
      this.now() - this.maxAgeMs,
    );
    for (const [id, ts] of this.seen) {
      if (ts < keepFrom) {
        this.seen.delete(id);
      }
    }
    if (this.seen.size <= this.maxSeen) {
      return;
    }
    const oldestFirst = [...this.seen].sort((a, b) => a[1] - b[1]);
    const dropped = oldestFirst.slice(0, oldestFirst.length - this.maxSeen);
    for (const [id] of dropped) {
      this.seen.delete(id);
    }
    this.recoveryFloor = Math.max(this.recoveryFloor, (dropped.at(-1)?.[1] ?? 0) + 1);
  }
}
