// Process and wire adapter for the hkcli binary. No OpenClaw SDK imports here:
// this module is shared by the gateway, outbound sends, login and the tests.
import { spawn, type ChildProcess } from "node:child_process";
import { StringDecoder } from "node:string_decoder";

/** hkcli 0.4.1 keeps a store to one relay connection: replies go out through the running
 *  `hkcli sync`. With older versions a reply could open a second connection, after which Hike
 *  could stop delivering messages to the sync. */
export const MIN_HKCLI_VERSION = "0.4.1";
export const HKCLI_GO_INSTALL = "go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest";
export const HKCLI_RELEASES_URL = "https://github.com/vinaycodeaxe/hkcli/releases";
export const HKCLI_INSTALL_HINT =
  `Install hkcli ${MIN_HKCLI_VERSION} or newer: ${HKCLI_GO_INSTALL} ` +
  `(or download a release from ${HKCLI_RELEASES_URL}), then set ` +
  "channels.hike.cliPath if it is not on PATH.";

/** How to start hkcli. `cliArgs` are prepended to every invocation (tests run a script). */
export type HkcliCommand = { cliPath: string; cliArgs?: readonly string[] };

export type HkcliEnvelope =
  | { success: true; data: unknown }
  | { success: false; error: { code: string; message: string } };

export type HkcliRunResult = {
  started: boolean;
  exitCode: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
  aborted: boolean;
  spawnError?: NodeJS.ErrnoException;
};

const OUTPUT_LIMIT_CHARS = 4 * 1024 * 1024;

export function parseHkcliVersion(output: string): string | undefined {
  return /\bhkcli\s+v?(\d+\.\d+\.\d+)/i.exec(output)?.[1];
}

export function compareVersions(a: string, b: string): number {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i += 1) {
    const diff = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (diff !== 0) {
      return Math.sign(diff);
    }
  }
  return 0;
}

export function isSupportedHkcliVersion(version: string): boolean {
  return compareVersions(version, MIN_HKCLI_VERSION) >= 0;
}

/** Environment for hkcli children: --store always wins, and the plugin needs write access. */
export function hkcliEnv(base: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  const env = { ...base };
  delete env.HKCLI_STORE;
  delete env.HKCLI_READONLY;
  return env;
}

const storeArgs = (storeDir: string) => ["--store", storeDir, "--json"];

export const hkcliArgs = {
  version: () => ["--version"],
  sync: (storeDir: string) => [...storeArgs(storeDir), "sync"],
  auth: (storeDir: string) => [...storeArgs(storeDir), "auth"],
  authStatus: (storeDir: string, check: boolean) => [
    ...storeArgs(storeDir),
    "auth",
    "status",
    ...(check ? ["--check"] : []),
  ],
  authLogout: (storeDir: string) => [...storeArgs(storeDir), "auth", "logout"],
  sendText: (storeDir: string, to: string, replyTo?: string) => [
    ...storeArgs(storeDir),
    "send",
    "text",
    "--to",
    to,
    "--message",
    "-",
    ...(replyTo ? ["--reply-to", replyTo] : []),
  ],
  chatsList: (storeDir: string, limit: number) => [
    ...storeArgs(storeDir),
    "chats",
    "list",
    "--direct",
    "--limit",
    String(limit),
  ],
  messagesList: (storeDir: string, chat: string, limit: number, before?: number) => [
    ...storeArgs(storeDir),
    "messages",
    "list",
    "--chat",
    chat,
    "--limit",
    String(limit),
    ...(before === undefined ? [] : ["--before", String(before)]),
  ],
};

/** Splits a byte stream into JSON lines, tolerating chunks that end mid-character or mid-line. */
export class NdjsonDecoder {
  private readonly decoder = new StringDecoder("utf8");
  private buffer = "";
  /** The current line already went over the limit: drop it up to its newline. */
  private skipping = false;
  private readonly onValue: (value: unknown) => void;
  private readonly onMalformed: (bytes: number) => void;
  private readonly maxLineChars: number;

  constructor(
    onValue: (value: unknown) => void,
    onMalformed: (bytes: number) => void = () => {},
    maxLineChars = 8 * 1024 * 1024,
  ) {
    this.onValue = onValue;
    this.onMalformed = onMalformed;
    this.maxLineChars = maxLineChars;
  }

  push(chunk: Buffer | string): void {
    this.buffer += typeof chunk === "string" ? chunk : this.decoder.write(chunk);
    let newline = this.buffer.indexOf("\n");
    while (newline >= 0) {
      const line = this.buffer.slice(0, newline);
      this.buffer = this.buffer.slice(newline + 1);
      if (this.skipping) {
        this.skipping = false;
      } else {
        this.emit(line);
      }
      newline = this.buffer.indexOf("\n");
    }
    if (this.buffer.length > this.maxLineChars) {
      if (!this.skipping) {
        this.onMalformed(Buffer.byteLength(this.buffer));
      }
      this.skipping = true;
      this.buffer = "";
    }
  }

  end(): void {
    this.buffer += this.decoder.end();
    const rest = this.buffer;
    this.buffer = "";
    if (!this.skipping) {
      this.emit(rest);
    }
    this.skipping = false;
  }

  private emit(raw: string): void {
    if (raw.length > this.maxLineChars) {
      this.onMalformed(Buffer.byteLength(raw));
      return;
    }
    const line = raw.trim();
    if (!line) {
      return;
    }
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch {
      this.onMalformed(Buffer.byteLength(line));
      return;
    }
    this.onValue(value);
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reads a `{success, data, error}` envelope; one-shot commands print it as their last line. */
export function readEnvelope(value: unknown): HkcliEnvelope | undefined {
  if (!isRecord(value) || typeof value.success !== "boolean") {
    return undefined;
  }
  if (value.success) {
    return { success: true, data: value.data };
  }
  const error = isRecord(value.error) ? value.error : {};
  return {
    success: false,
    error: {
      code: typeof error.code === "string" ? error.code : "error",
      message: typeof error.message === "string" ? error.message : "hkcli failed",
    },
  };
}

export function parseEnvelope(stdout: string): HkcliEnvelope | undefined {
  const lines = stdout.split("\n");
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    const line = lines[i]?.trim();
    if (!line) {
      continue;
    }
    try {
      return readEnvelope(JSON.parse(line));
    } catch {
      return undefined;
    }
  }
  return undefined;
}

export type HkcliStdio = {
  stdin?: "pipe" | "ignore" | "inherit";
  stdout?: "pipe" | "inherit";
  stderr?: "pipe" | "inherit" | "ignore";
};

export function spawnHkcli(
  command: HkcliCommand,
  args: readonly string[],
  options: HkcliStdio & { env?: NodeJS.ProcessEnv } = {},
): ChildProcess {
  return spawn(command.cliPath, [...(command.cliArgs ?? []), ...args], {
    stdio: [options.stdin ?? "ignore", options.stdout ?? "pipe", options.stderr ?? "pipe"],
    env: options.env ?? hkcliEnv(),
    windowsHide: true,
  });
}

/** Runs a short hkcli command with a deadline; never throws. */
export function runHkcli(
  command: HkcliCommand,
  args: readonly string[],
  options: { input?: string; timeoutMs: number; signal?: AbortSignal; env?: NodeJS.ProcessEnv },
): Promise<HkcliRunResult> {
  return new Promise((resolve) => {
    const result: HkcliRunResult = {
      started: false,
      exitCode: null,
      signal: null,
      stdout: "",
      stderr: "",
      timedOut: false,
      aborted: false,
    };
    if (options.signal?.aborted) {
      resolve({ ...result, aborted: true });
      return;
    }
    let child: ChildProcess;
    try {
      child = spawnHkcli(command, args, {
        stdin: options.input === undefined ? "ignore" : "pipe",
        env: options.env,
      });
    } catch (error) {
      resolve({ ...result, spawnError: error as NodeJS.ErrnoException });
      return;
    }
    const stdout = new StringDecoder("utf8");
    const stderr = new StringDecoder("utf8");
    let settled = false;
    const finish = () => {
      if (settled) {
        return;
      }
      settled = true;
      clearTimeout(timer);
      options.signal?.removeEventListener("abort", onAbort);
      result.stdout += stdout.end();
      result.stderr += stderr.end();
      resolve(result);
    };
    const kill = () => {
      if (child.exitCode === null && child.signalCode === null) {
        child.kill("SIGKILL");
      }
    };
    const onAbort = () => {
      result.aborted = true;
      kill();
    };
    const timer = setTimeout(() => {
      result.timedOut = true;
      kill();
    }, options.timeoutMs);
    options.signal?.addEventListener("abort", onAbort, { once: true });
    child.on("spawn", () => {
      result.started = true;
    });
    child.stdout?.on("data", (chunk: Buffer) => {
      if (result.stdout.length < OUTPUT_LIMIT_CHARS) {
        result.stdout += stdout.write(chunk);
      }
    });
    child.stderr?.on("data", (chunk: Buffer) => {
      if (result.stderr.length < OUTPUT_LIMIT_CHARS) {
        result.stderr += stderr.write(chunk);
      }
    });
    child.on("error", (error: NodeJS.ErrnoException) => {
      result.spawnError ??= error;
      if (!result.started) {
        finish();
      }
    });
    child.on("close", (code, signal) => {
      result.exitCode = code;
      result.signal = signal;
      finish();
    });
    if (options.input !== undefined && child.stdin) {
      child.stdin.on("error", () => {});
      child.stdin.end(options.input);
    }
  });
}

export type HkcliVersionProbe =
  | { ok: true; version: string }
  | { ok: false; reason: "missing" | "too-old" | "failed"; version?: string; message: string };

export async function probeHkcliVersion(
  command: HkcliCommand,
  options: { timeoutMs?: number; env?: NodeJS.ProcessEnv } = {},
): Promise<HkcliVersionProbe> {
  const run = await runHkcli(command, hkcliArgs.version(), {
    timeoutMs: options.timeoutMs ?? 10_000,
    env: options.env,
  });
  if (!run.started && run.spawnError?.code === "ENOENT") {
    return {
      ok: false,
      reason: "missing",
      message: `hkcli was not found (${command.cliPath}). ${HKCLI_INSTALL_HINT}`,
    };
  }
  const version = parseHkcliVersion(`${run.stdout}\n${run.stderr}`);
  if (!version) {
    const detail = run.spawnError
      ? run.spawnError.message
      : run.timedOut
        ? "timed out"
        : `exit code ${String(run.exitCode)}`;
    return {
      ok: false,
      reason: "failed",
      message: `Could not run "${command.cliPath} --version" (${detail}). ${HKCLI_INSTALL_HINT}`,
    };
  }
  if (!isSupportedHkcliVersion(version)) {
    return {
      ok: false,
      reason: "too-old",
      version,
      message: `hkcli ${version} is too old; the Hike channel needs ${MIN_HKCLI_VERSION} or newer. ${HKCLI_INSTALL_HINT}`,
    };
  }
  return { ok: true, version };
}

export class HkcliCommandError extends Error {
  readonly code: string;
  readonly exitCode: number | null;
  constructor(message: string, code: string, exitCode: number | null) {
    super(message);
    this.name = "HkcliCommandError";
    this.code = code;
    this.exitCode = exitCode;
  }
}

/** Interprets a one-shot run: returns `data` on success, throws HkcliCommandError otherwise. */
export function expectEnvelopeData(run: HkcliRunResult, what: string): unknown {
  if (!run.started) {
    if (run.aborted) {
      throw new HkcliCommandError(`${what}: aborted before start`, "aborted", null);
    }
    const missing = run.spawnError?.code === "ENOENT";
    const detail = missing ? `hkcli not found. ${HKCLI_INSTALL_HINT}` : run.spawnError?.message;
    throw new HkcliCommandError(`${what}: ${detail ?? "could not start hkcli"}`, missing ? "hkcli_missing" : "spawn_failed", null);
  }
  const envelope = parseEnvelope(run.stdout);
  if (envelope?.success) {
    return envelope.data;
  }
  if (envelope) {
    throw new HkcliCommandError(`${what}: ${envelope.error.message}`, envelope.error.code, run.exitCode);
  }
  if (run.exitCode === 2) {
    throw new HkcliCommandError(`${what}: hkcli rejected the arguments`, "usage", run.exitCode);
  }
  const reason = run.timedOut
    ? "timed out"
    : run.aborted
      ? "aborted"
      : `exit ${String(run.exitCode ?? run.signal)}`;
  throw new HkcliCommandError(`${what}: no result from hkcli (${reason})`, "no_result", run.exitCode);
}

/** Hike user id of the login saved in the store, from the local `auth status` (no network). */
export async function readHkcliUserId(
  command: HkcliCommand,
  storeDir: string,
  options: { timeoutMs?: number; signal?: AbortSignal; env?: NodeJS.ProcessEnv } = {},
): Promise<string> {
  const run = await runHkcli(command, hkcliArgs.authStatus(storeDir, false), {
    timeoutMs: options.timeoutMs ?? 15_000,
    signal: options.signal,
    env: options.env,
  });
  const data = expectEnvelopeData(run, "hkcli auth status");
  const userId = isRecord(data) && typeof data.user_id === "string" ? data.user_id.trim() : "";
  if (!userId) {
    throw new HkcliCommandError("hkcli auth status: no Hike login is saved in this store", "not_logged_in", run.exitCode);
  }
  return userId;
}

/** hkcli error codes that prove nothing was published to Hike. */
const NOT_SENT_CODES = new Set([
  "invalid_input",
  "not_found",
  "ambiguous",
  "read_only",
  "auth_required",
  "usage",
  "aborted",
  "hkcli_missing",
  "spawn_failed",
  // hkcli could not get the store's relay connection (busy, or Hike unreachable); retrying is safe.
  "relay_unavailable",
]);

export type HkcliSendReceipt = {
  to: string;
  clientId?: string;
  serverId?: string;
  ts?: number;
};

export class HkcliSendError extends Error {
  readonly code: string;
  /** False only when hkcli proved the message never left this machine. */
  readonly maybeSent: boolean;
  constructor(message: string, code: string, maybeSent: boolean) {
    super(message);
    this.name = "HkcliSendError";
    this.code = code;
    this.maybeSent = maybeSent;
  }
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

export function readSendReceipt(data: unknown, fallbackTo: string): HkcliSendReceipt {
  const record = isRecord(data) ? data : {};
  return {
    to: optionalString(record.to) ?? fallbackTo,
    clientId: optionalString(record.client_id),
    serverId: optionalString(record.server_id),
    ts: typeof record.ts === "number" ? record.ts : undefined,
  };
}

/** Sends one text message through `hkcli send text`, with the text on stdin. */
export async function sendHkcliText(
  command: HkcliCommand,
  params: {
    storeDir: string;
    to: string;
    text: string;
    replyTo?: string;
    timeoutMs?: number;
    signal?: AbortSignal;
    env?: NodeJS.ProcessEnv;
  },
): Promise<HkcliSendReceipt> {
  const run = await runHkcli(command, hkcliArgs.sendText(params.storeDir, params.to, params.replyTo), {
    input: params.text,
    timeoutMs: params.timeoutMs ?? 90_000,
    signal: params.signal,
    env: params.env,
  });
  try {
    return readSendReceipt(expectEnvelopeData(run, "hkcli send"), params.to);
  } catch (error) {
    if (!(error instanceof HkcliCommandError)) {
      throw error;
    }
    throw new HkcliSendError(error.message, error.code, !NOT_SENT_CODES.has(error.code));
  }
}

/** True when a failed `send --reply-to` only failed because the quoted message is unknown locally. */
export function isMissingReplyTarget(error: unknown): boolean {
  return (
    error instanceof HkcliSendError &&
    error.code === "not_found" &&
    /\bmessage\b.*\bnot found\b/i.test(error.message)
  );
}
