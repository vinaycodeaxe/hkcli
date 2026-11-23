// Login, logout and live probe for one Hike account, all through hkcli.
import { mkdirSync } from "node:fs";
import { DEFAULT_ACCOUNT_ID } from "openclaw/plugin-sdk/account-id";
import type { RuntimeEnv } from "openclaw/plugin-sdk/runtime-env";
import {
  HKCLI_GO_INSTALL,
  HKCLI_INSTALL_HINT,
  expectEnvelopeData,
  hkcliArgs,
  isRecord,
  parseEnvelope,
  probeHkcliVersion,
  runHkcli,
  spawnHkcli,
} from "./hkcli.ts";
import { hkcliCommandFor, isHikeAccountLinked, type ResolvedHikeAccount } from "./accounts.ts";
import { forgetHikeCursor } from "./inbound-state.ts";
import { getOptionalHikeRuntime } from "./runtime.ts";

export type HikeProbe = {
  ok: boolean;
  hkcliVersion?: string;
  linked?: boolean;
  userId?: string;
  name?: string;
  serverCheck?: string;
  /** Operator-facing problem and fix when ok is false. */
  problem?: { kind: "config" | "auth" | "runtime"; message: string; fix?: string };
};

function accountFlag(accountId: string): string {
  return accountId === DEFAULT_ACCOUNT_ID ? "" : ` --account ${accountId}`;
}

export function loginCommand(accountId: string): string {
  return `openclaw channels login --channel hike${accountFlag(accountId)}`;
}

export function stopAccountCommand(accountId: string): string {
  return `openclaw gateway call channels.stop --params '{"channel":"hike","accountId":"${accountId}"}'`;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function runInTerminal(account: ResolvedHikeAccount): Promise<{ code: number | null; stdout: string; spawnError?: NodeJS.ErrnoException }> {
  return new Promise((resolve) => {
    let stdout = "";
    const child = spawnHkcli(hkcliCommandFor(account), hkcliArgs.auth(account.storeDir), {
      stdin: "inherit",
      stdout: "pipe",
      stderr: "inherit",
    });
    child.stdout?.setEncoding("utf8");
    child.stdout?.on("data", (chunk: string) => {
      stdout += chunk;
    });
    child.on("error", (error: NodeJS.ErrnoException) => resolve({ code: null, stdout, spawnError: error }));
    child.on("close", (code) => resolve({ code, stdout }));
  });
}

/**
 * `openclaw channels login --channel hike`: hkcli shows the QR code on the terminal (stderr)
 * and prints its JSON result on stdout once the phone confirms.
 */
export async function loginHikeAccount(account: ResolvedHikeAccount, runtime: RuntimeEnv): Promise<void> {
  const version = await probeHkcliVersion(hkcliCommandFor(account));
  if (!version.ok) {
    throw new Error(version.message);
  }
  mkdirSync(account.storeDir, { recursive: true, mode: 0o700 });
  runtime.log(`Linking Hike account "${account.accountId}" with hkcli ${version.version} (store: ${account.storeDir}).`);
  runtime.log(
    "Use the phone with the Hike number the agent should own: Hike > Settings > Web QR Login, then scan the code.",
  );
  runtime.log(
    `To log in with an SMS code instead, run: ${account.cliPath} --store "${account.storeDir}" auth --phone +<country code><number>`,
  );
  const result = await runInTerminal(account);
  if (result.spawnError) {
    throw new Error(`Could not start hkcli (${result.spawnError.message}). ${HKCLI_INSTALL_HINT}`);
  }
  const envelope = parseEnvelope(result.stdout);
  if (envelope?.success) {
    const data = isRecord(envelope.data) ? envelope.data : {};
    const who = text(data.name) ?? text(data.phone) ?? "your Hike account";
    runtime.log(`Linked Hike account "${account.accountId}" as ${who} (user id ${String(data.user_id ?? "?")}).`);
    return;
  }
  if (envelope?.error.code === "already_logged_in") {
    runtime.log(
      `Hike account "${account.accountId}" is already linked: ${envelope.error.message.replace(/; run .*$/, "")}. ` +
        `To link another number, run: openclaw channels logout --channel hike${accountFlag(account.accountId)}, then log in again.`,
    );
    return;
  }
  if (envelope?.error.code === "store_locked") {
    throw new Error(
      `The Hike account "${account.accountId}" is running (hkcli sync holds its store). Stop it first with: ` +
        `${stopAccountCommand(account.accountId)} (or stop the gateway), then run this login again.`,
    );
  }
  if (envelope) {
    throw new Error(`Hike login failed (${envelope.error.code}): ${envelope.error.message}`);
  }
  throw new Error(result.code === 130 ? "Hike login cancelled." : `hkcli auth exited with code ${String(result.code)}.`);
}

export async function logoutHikeAccount(
  account: ResolvedHikeAccount,
): Promise<{ cleared: boolean; loggedOut: boolean; serverLogout?: string }> {
  const run = await runHkcli(hkcliCommandFor(account), hkcliArgs.authLogout(account.storeDir), {
    timeoutMs: 60_000,
  });
  const data = expectEnvelopeData(run, "hkcli auth logout");
  // Whichever number is linked next must not inherit where this one stopped.
  await forgetHikeCursor(getOptionalHikeRuntime(), account);
  const record = isRecord(data) ? data : {};
  const cleared = record.logged_out === true;
  const serverLogout = text(record.server_logout);
  return {
    cleared,
    loggedOut: cleared && (serverLogout === "ok" || serverLogout === "skipped"),
    ...(serverLogout ? { serverLogout } : {}),
  };
}

/** Bounded live check: hkcli version, then `auth status --check` (asks Hike once). */
export async function probeHikeAccount(account: ResolvedHikeAccount, timeoutMs: number): Promise<HikeProbe> {
  const deadline = Date.now() + Math.max(1_000, timeoutMs);
  const version = await probeHkcliVersion(hkcliCommandFor(account), {
    timeoutMs: Math.min(10_000, Math.max(1_000, timeoutMs)),
  });
  if (!version.ok) {
    return {
      ok: false,
      problem: {
        kind: "config",
        message: version.message,
        fix: HKCLI_GO_INSTALL,
      },
    };
  }
  if (!isHikeAccountLinked(account)) {
    return {
      ok: false,
      hkcliVersion: version.version,
      linked: false,
      problem: { kind: "auth", message: "Hike account is not linked.", fix: loginCommand(account.accountId) },
    };
  }
  const run = await runHkcli(hkcliCommandFor(account), hkcliArgs.authStatus(account.storeDir, true), {
    timeoutMs: Math.max(1_000, deadline - Date.now()),
  });
  try {
    const data = expectEnvelopeData(run, "hkcli auth status");
    const record = isRecord(data) ? data : {};
    const loggedIn = record.logged_in === true;
    const serverCheck = text(record.server_check);
    return {
      ok: loggedIn && serverCheck === "ok",
      hkcliVersion: version.version,
      linked: loggedIn,
      userId: text(record.user_id),
      name: text(record.name),
      serverCheck,
      ...(loggedIn
        ? {}
        : {
            problem: {
              kind: "auth" as const,
              message: `Hike login no longer works (${text(record.revoked) ?? serverCheck ?? "logged out"}).`,
              fix: loginCommand(account.accountId),
            },
          }),
    };
  } catch (error) {
    return {
      ok: false,
      hkcliVersion: version.version,
      linked: true,
      problem: { kind: "runtime", message: error instanceof Error ? error.message : String(error) },
    };
  }
}
