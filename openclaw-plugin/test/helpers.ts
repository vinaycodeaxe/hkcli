import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { HkcliCommand } from "../src/hkcli.ts";

const here = path.dirname(fileURLToPath(import.meta.url));

export const fakeHkcli: HkcliCommand = {
  cliPath: process.execPath,
  cliArgs: [path.join(here, "fixtures", "fake-hkcli.mjs")],
};

export const missingHkcli: HkcliCommand = { cliPath: path.join(here, "fixtures", "no-such-hkcli") };

/** Scratch directory inside the package (ignored by git), removed by the returned cleanup. */
export function scratchDir(): { dir: string; cleanup: () => void } {
  const root = path.join(here, "..", ".test-tmp");
  mkdirSync(root, { recursive: true });
  const dir = mkdtempSync(path.join(root, "t-"));
  return { dir, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

export function fakeEnv(extra: Record<string, string>): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env, ...extra };
  delete env.HKCLI_STORE;
  delete env.HKCLI_READONLY;
  return env;
}

export function isRunning(pid: number | undefined): boolean {
  if (!pid) {
    return false;
  }
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export async function waitFor(predicate: () => boolean, timeoutMs = 5_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) {
      throw new Error("timed out waiting for condition");
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}
