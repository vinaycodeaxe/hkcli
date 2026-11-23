// Decides which inbound messages are new: drops our own echoes, duplicates and history, and
// keeps the cursor that lets a restart (or a reconnect) answer what arrived while sync was down.

export type InboundCursorState = {
  v: 2;
  /** Hike user id the state belongs to (missing in state saved by 0.1.0). */
  user?: string;
  /** Time (epoch ms) up to which every inbound message was handled or deliberately skipped. */
  cursor: number;
  /** Read-backs never go below this; raised only when `seen` overflowed, and never past `cursor`. */
  floor?: number;
  /** Handled direct messages ([id, ts]) newer than the oldest bound a read-back may still use. */
  seen: Array<[string, number]>;
};

export type InboundCursorStore = {
  load(): Promise<unknown>;
  save(state: InboundCursorState): Promise<void>;
};

export type InboundGateDecision = "accept" | "self" | "duplicate" | "stale" | "future";

export type GateMessage = { id: string; fromMe: boolean; ts: number; chatType: "direct" | "group" };

export type InboundGateOptions = {
  store: InboundCursorStore;
  /** Hike user id of the linked account: saved state of another user is discarded. */
  user?: string;
  onUserChanged?: (previous: string, current: string) => void;
  /** Slack for sender clocks and delivery delays around the cursor. */
  graceMs?: number;
  /** Messages older than this are never answered, however long sync was down. */
  maxAgeMs?: number;
  /** Hard cap on remembered ids (plugin state values are limited to 64 KiB). */
  maxSeen?: number;
  saveDelayMs?: number;
  now?: () => number;
  /** A save failed; the state stays dirty and the next save retries. */
  onError?: (error: unknown) => void;
};

export const DEFAULT_CURSOR_GRACE_MS = 5 * 60_000;
export const DEFAULT_MAX_RECOVERY_AGE_MS = 12 * 60 * 60_000;
const DEFAULT_MAX_SEEN = 800;
const RECENT_GROUP_IDS = 1000;

type LoadedState = { user?: string; cursor: number; floor: number; seen: Map<string, number> };

function finite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function readState(value: unknown): LoadedState | undefined {
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  const state = value as { v?: unknown; user?: unknown; cursor?: unknown; floor?: unknown; seen?: unknown };
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
    return {
      ...(typeof state.user === "string" ? { user: state.user } : {}),
      cursor: state.cursor,
      floor: finite(state.floor) ? state.floor : 0,
      seen,
    };
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
  private readonly user: string | undefined;
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
    this.user = options.user ?? state?.user;
    this.now = options.now ?? Date.now;
    this.graceMs = options.graceMs ?? DEFAULT_CURSOR_GRACE_MS;
    this.maxAgeMs = options.maxAgeMs ?? DEFAULT_MAX_RECOVERY_AGE_MS;
    this.maxSeen = options.maxSeen ?? DEFAULT_MAX_SEEN;
    this.saveDelayMs = options.saveDelayMs ?? 1000;
    this.onError = options.onError ?? (() => {});
    const openedAt = this.now();
    this.cursor = state ? Math.min(state.cursor, openedAt) : openedAt;
    this.liveFloor = this.cursor - this.graceMs;
    this.recoveryFloor = Math.min(state?.floor ?? 0, this.cursor);
    this.seen = state?.seen ?? new Map();
    // A first run, or state from before the user was recorded, is saved right away.
    this.dirty = !state || state.user !== this.user;
  }

  /**
   * Loads the saved state. A failed lookup is thrown: starting from a fresh cursor instead would
   * drop every message that arrived while the account was down. Unreadable state, or state of
   * another Hike user, starts from now like a first run, and that new state must be saved before
   * the gate opens: until it is, a restart would start from "now" again and skip the downtime.
   */
  static async open(options: InboundGateOptions): Promise<InboundGate> {
    let state: LoadedState | undefined;
    try {
      state = readState(await options.store.load());
    } catch (error) {
      throw new Error(`could not read the saved inbound cursor: ${errorText(error)}`, { cause: error });
    }
    const { user } = options;
    if (state?.user !== undefined && user !== undefined && state.user !== user) {
      options.onUserChanged?.(state.user, user);
      state = undefined;
    }
    const gate = new InboundGate(options, state);
    if (!state) {
      gate.dirty = false;
      try {
        await options.store.save(gate.snapshot());
      } catch (error) {
        throw new Error(`could not save the new inbound cursor: ${errorText(error)}`, { cause: error });
      }
    } else if (gate.dirty) {
      // Adopting state saved before users were recorded: a failed save loses nothing.
      await gate.flush();
    }
    return gate;
  }

  get cursorValue(): number {
    return this.cursor;
  }

  /**
   * Live events (no `bound`) use the run's floor. Read-backs pass the bound they read from, and the
   * floor the cap may have raised since applies too. Read-backs also skip messages dated more than
   * the grace period ahead: the sender sets that date, so it cannot be placed against the cursor.
   * Live, such a message is still answered once.
   */
  check(message: GateMessage, bound?: number): InboundGateDecision {
    if (message.fromMe) {
      return "self";
    }
    if (this.seen.has(message.id) || this.pending.has(message.id) || this.recentGroupIds.has(message.id)) {
      return "duplicate";
    }
    const now = this.now();
    if (bound !== undefined && message.ts > now + this.graceMs) {
      return "future";
    }
    const lower = bound === undefined ? this.liveFloor : Math.max(bound, this.recoveryFloor);
    if (message.ts < lower || message.ts < now - this.maxAgeMs) {
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
      ...(this.user !== undefined ? { user: this.user } : {}),
      cursor: this.cursor,
      ...(this.recoveryFloor > 0 ? { floor: this.recoveryFloor } : {}),
      seen,
    };
  }

  /**
   * Forgets ids no read-back can offer again: older than every bound in use (cursor - grace,
   * running read-backs) or than the age limit, or dated more than the age limit ahead. Past
   * `maxSeen`, ids dated in the future go first (read-backs skip those anyway), then the oldest,
   * and read-backs start above the dropped ones, though never above the cursor.
   */
  private prune(): void {
    const now = this.now();
    const keepFrom = Math.max(Math.min(this.cursor - this.graceMs, ...this.activeBounds), now - this.maxAgeMs);
    for (const [id, ts] of this.seen) {
      if (ts < keepFrom || ts > now + this.maxAgeMs) {
        this.seen.delete(id);
      }
    }
    const excess = this.seen.size - this.maxSeen;
    if (excess <= 0) {
      return;
    }
    const future = now + this.graceMs;
    // Future-dated ids first, latest first; then the oldest.
    const dropped = [...this.seen]
      .sort(([, a], [, b]) => (a > future || b > future ? b - a : a - b))
      .slice(0, excess);
    for (const [id] of dropped) {
      this.seen.delete(id);
    }
    const past = dropped.filter(([, ts]) => ts <= future);
    if (past.length > 0) {
      const above = Math.max(...past.map(([, ts]) => ts)) + 1;
      this.recoveryFloor = Math.max(this.recoveryFloor, Math.min(above, this.cursor));
    }
  }
}
