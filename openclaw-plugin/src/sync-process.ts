// Remembers which `hkcli sync` this plugin started for an account, so a sync left running by a
// Gateway that died can be stopped by the next run, and nothing else ever is.

export type SyncProcessRecord = { pid: number; storeDir: string; startedAt: number };

export type SyncProcessRecordStore = {
  lookup(key: string): Promise<unknown>;
  register(key: string, value: SyncProcessRecord): Promise<unknown>;
  delete(key: string): Promise<unknown>;
};

type Kill = (pid: number, signal: NodeJS.Signals) => void;

function readRecord(value: unknown): SyncProcessRecord | undefined {
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  const record = value as Partial<SyncProcessRecord>;
  return typeof record.pid === "number" && Number.isInteger(record.pid) && typeof record.storeDir === "string"
    ? { pid: record.pid, storeDir: record.storeDir, startedAt: Number(record.startedAt) || 0 }
    : undefined;
}

export class SyncProcessTracker {
  private readonly store: SyncProcessRecordStore;
  private readonly key: string;
  private readonly storeDir: string;
  private readonly kill: Kill;
  private readonly now: () => number;
  /** Read once before this run spawns anything: its own children never overwrite it. */
  private previous: SyncProcessRecord | undefined;
  private registered: number | undefined;

  private constructor(params: { store: SyncProcessRecordStore; key: string; storeDir: string; kill?: Kill; now?: () => number }) {
    this.store = params.store;
    this.key = params.key;
    this.storeDir = params.storeDir;
    this.kill = params.kill ?? ((pid, signal) => process.kill(pid, signal));
    this.now = params.now ?? Date.now;
  }

  static async open(params: {
    store: SyncProcessRecordStore;
    key: string;
    storeDir: string;
    kill?: Kill;
    now?: () => number;
  }): Promise<SyncProcessTracker> {
    const tracker = new SyncProcessTracker(params);
    tracker.previous = readRecord(await params.store.lookup(params.key).catch(() => undefined));
    return tracker;
  }

  /** The child printed its first event, so it holds the store lock: remember it. */
  lockHeld(pid: number | undefined): void {
    if (!pid || pid === this.registered) {
      return;
    }
    this.registered = pid;
    void this.store.register(this.key, { pid, storeDir: this.storeDir, startedAt: this.now() }).catch(() => undefined);
  }

  /**
   * hkcli reported `holderPid` as the holder of the store lock. Stops it only when an earlier
   * run of this plugin started that very process for this same store.
   */
  reclaim(holderPid: number | undefined): boolean {
    const previous = this.previous;
    if (
      !holderPid ||
      holderPid === process.pid ||
      previous === undefined ||
      previous.pid !== holderPid ||
      previous.storeDir !== this.storeDir
    ) {
      return false;
    }
    try {
      this.kill(holderPid, "SIGKILL");
      this.previous = undefined;
      return true;
    } catch {
      return false;
    }
  }

  /** This run's children have exited; a record from an earlier run that was never reclaimed stays. */
  async release(): Promise<void> {
    if (this.registered !== undefined) {
      await this.store.delete(this.key).catch(() => undefined);
    }
  }
}
