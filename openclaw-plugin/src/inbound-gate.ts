// Decides which inbound messages are new: drops our own echoes, duplicates, and history older
// than the persisted cursor, so a restart answers what arrived while the gateway was down.

export type InboundCursorState = {
  v: 1;
  /** Time (epoch ms, local clock) up to which every inbound message was handled. */
  cursor: number;
  /** Ids of recently handled messages, oldest first. */
  seen: string[];
};

export type InboundCursorStore = {
  load(): Promise<InboundCursorState | undefined>;
  save(state: InboundCursorState): Promise<void>;
};

export type InboundGateDecision = "accept" | "self" | "duplicate" | "stale";

export type InboundGateOptions = {
  store: InboundCursorStore;
  /** Messages older than (persisted cursor - graceMs) are history; the first start uses now. */
  graceMs?: number;
  maxSeen?: number;
  saveDelayMs?: number;
  now?: () => number;
  onError?: (error: unknown) => void;
};

export const DEFAULT_CURSOR_GRACE_MS = 5 * 60_000;

function isState(value: unknown): value is InboundCursorState {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const state = value as Partial<InboundCursorState>;
  return (
    state.v === 1 &&
    typeof state.cursor === "number" &&
    Number.isFinite(state.cursor) &&
    Array.isArray(state.seen)
  );
}

export class InboundGate {
  /** Fixed for the lifetime of this gate (one account run). */
  readonly threshold: number;
  private cursor: number;
  private readonly seen: Set<string>;
  /** Accepted but not yet handled; never persisted, so a restart picks them up again. */
  private readonly pending = new Map<string, number>();
  private readonly store: InboundCursorStore;
  private readonly maxSeen: number;
  private readonly saveDelayMs: number;
  private readonly now: () => number;
  private readonly onError: (error: unknown) => void;
  private dirty = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private writing: Promise<void> | undefined;

  private constructor(options: InboundGateOptions, state: InboundCursorState | undefined) {
    this.store = options.store;
    this.now = options.now ?? Date.now;
    // Keyed plugin state values are capped at 64 KiB; 500 ids stay far below that.
    this.maxSeen = options.maxSeen ?? 500;
    this.saveDelayMs = options.saveDelayMs ?? 1000;
    this.onError = options.onError ?? (() => {});
    const started = this.now();
    this.cursor = state ? Math.min(state.cursor, started) : started;
    this.threshold = this.cursor - (options.graceMs ?? DEFAULT_CURSOR_GRACE_MS);
    this.seen = new Set(state?.seen.filter((id) => typeof id === "string") ?? []);
    this.trimSeen();
    this.dirty = !state;
  }

  static async open(options: InboundGateOptions): Promise<InboundGate> {
    let state: InboundCursorState | undefined;
    try {
      const loaded = await options.store.load();
      state = isState(loaded) ? loaded : undefined;
    } catch (error) {
      options.onError?.(error);
    }
    const gate = new InboundGate(options, state);
    if (gate.dirty) {
      await gate.flush();
    }
    return gate;
  }

  get cursorValue(): number {
    return this.cursor;
  }

  check(message: { id: string; fromMe: boolean; ts: number }): InboundGateDecision {
    if (message.fromMe) {
      return "self";
    }
    if (this.seen.has(message.id) || this.pending.has(message.id)) {
      return "duplicate";
    }
    if (message.ts < this.threshold) {
      return "stale";
    }
    this.pending.set(message.id, message.ts);
    return "accept";
  }

  /** Marks an accepted message as handled; call before its (non-repeatable) side effects. */
  complete(id: string): void {
    const ts = this.pending.get(id);
    if (ts === undefined) {
      return;
    }
    this.pending.delete(id);
    this.seen.add(id);
    this.trimSeen();
    this.dirty = true;
    this.advance(ts);
    this.scheduleSave();
  }

  /** Forgets an accepted message without handling it, so a later catch-up may offer it again. */
  release(id: string): void {
    this.pending.delete(id);
  }

  /** Everything before `time` was handled (for example after a complete catch-up). */
  advance(time: number): void {
    let next = Math.min(time, this.now());
    for (const ts of this.pending.values()) {
      next = Math.min(next, ts - 1);
    }
    if (next > this.cursor) {
      this.cursor = next;
      this.scheduleSave();
    }
  }

  async flush(): Promise<void> {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    while (this.writing) {
      await this.writing;
    }
    if (!this.dirty) {
      return;
    }
    this.dirty = false;
    const state: InboundCursorState = { v: 1, cursor: this.cursor, seen: [...this.seen] };
    this.writing = this.store.save(state).then(
      () => undefined,
      (error: unknown) => {
        this.dirty = true;
        this.onError(error);
      },
    );
    try {
      await this.writing;
    } finally {
      this.writing = undefined;
    }
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

  private trimSeen(): void {
    while (this.seen.size > this.maxSeen) {
      const oldest = this.seen.values().next().value;
      if (oldest === undefined) {
        return;
      }
      this.seen.delete(oldest);
    }
  }
}
