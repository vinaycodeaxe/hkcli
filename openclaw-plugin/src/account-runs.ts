// One run per Hike account at a time. A new run (reload, restart, relink) first stops the run it
// replaces and waits for it, so the old run's final saves can never land on top of the new run's.

export type AccountRun = {
  readonly abort: AbortController;
  /** Resolves when this run has stopped; it waited for the run it replaced first. */
  readonly done: Promise<void>;
  /** Waits up to `timeoutMs` for the replaced run to stop; false if it is still stopping. */
  previousStopped(timeoutMs: number): Promise<boolean>;
  /** Marks this run stopped, so a replacement or a logout waiting for it can go on. */
  finish(): void;
};

async function settlesWithin(promise: Promise<unknown>, timeoutMs: number): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise.then(() => true),
      new Promise<boolean>((resolve) => {
        timer = setTimeout(() => resolve(false), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

export class AccountRuns {
  private readonly runs = new Map<string, AccountRun>();

  current(accountId: string): AccountRun | undefined {
    return this.runs.get(accountId);
  }

  /** Registers a new run and asks the run it replaces to stop. */
  begin(accountId: string): AccountRun {
    const previous = this.runs.get(accountId);
    previous?.abort.abort();
    let finished: () => void = () => {};
    const done = new Promise<void>((resolve) => {
      finished = resolve;
    });
    const run: AccountRun = {
      abort: new AbortController(),
      done,
      previousStopped: async (timeoutMs) => (previous ? await settlesWithin(previous.done, timeoutMs) : true),
      finish: () => {
        if (this.runs.get(accountId) === run) {
          this.runs.delete(accountId);
        }
        finished();
      },
    };
    this.runs.set(accountId, run);
    return run;
  }
}
