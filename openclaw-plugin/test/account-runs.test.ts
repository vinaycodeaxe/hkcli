import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { AccountRuns } from "../src/account-runs.ts";

const turn = () => new Promise((resolve) => setImmediate(resolve));

describe("AccountRuns", () => {
  it("stops the run it replaces and waits for it, without ever removing the newer run", async () => {
    const runs = new AccountRuns();
    const a = runs.begin("default");
    assert.equal(await a.previousStopped(10), true);
    const b = runs.begin("default");
    assert.equal(a.abort.signal.aborted, true);
    assert.equal(runs.current("default"), b);

    let stopped: boolean | undefined;
    const waiting = b.previousStopped(5_000).then((value) => {
      stopped = value;
    });
    await turn();
    assert.equal(stopped, undefined);
    a.finish();
    await waiting;
    assert.equal(stopped, true);
    assert.equal(runs.current("default"), b);
    b.finish();
    assert.equal(runs.current("default"), undefined);
  });

  it("waits for every run in a chain of quick replacements, up to the bound", async () => {
    const runs = new AccountRuns();
    const a = runs.begin("default");
    const start = (run: ReturnType<AccountRuns["begin"]>) =>
      (async () => {
        await run.previousStopped(5_000);
        run.finish();
      })();
    const b = runs.begin("default");
    const bStopped = start(b);
    const c = runs.begin("default");
    assert.equal(b.abort.signal.aborted, true);
    let cWaited = false;
    const cWaiting = c.previousStopped(5_000).then(() => {
      cWaited = true;
    });
    await turn();
    assert.equal(cWaited, false);
    a.finish();
    await bStopped;
    await cWaiting;
    assert.equal(cWaited, true);

    const stuck = runs.begin("other");
    const next = runs.begin("other");
    assert.equal(await next.previousStopped(20), false);
    stuck.finish();
    assert.equal(runs.current("other"), next);
  });
});
