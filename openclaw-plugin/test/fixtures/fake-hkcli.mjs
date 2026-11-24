#!/usr/bin/env node
// Stand-in for the hkcli binary in unit tests. Behaviour is chosen with FAKE_HKCLI_* variables.
import { appendFileSync } from "node:fs";

const args = process.argv.slice(2);
const env = process.env;

function log(entry) {
  if (env.FAKE_HKCLI_LOG) {
    appendFileSync(env.FAKE_HKCLI_LOG, `${JSON.stringify(entry)}\n`);
  }
}

function print(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function fail(code, message, exitCode = 1) {
  print({ success: false, data: null, error: { code, message } });
  process.exit(exitCode);
}

function readStdin() {
  return new Promise((resolve) => {
    let data = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => {
      data += chunk;
    });
    process.stdin.on("end", () => resolve(data));
  });
}

function option(name) {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
}

const VALUE_FLAGS = new Set(["--store", "--to", "--message", "--reply-to", "--limit", "--chat", "--before"]);
const command = args
  .filter((arg, index) => !arg.startsWith("--") && !VALUE_FLAGS.has(args[index - 1]))
  .join(" ");

if (args.includes("--version")) {
  process.stdout.write(`hkcli ${env.FAKE_HKCLI_VERSION ?? "0.4.1"}\n`);
  process.exit(0);
}

if (command === "send text") {
  const text = await readStdin();
  const to = option("--to");
  const replyTo = option("--reply-to");
  log({ command, to, replyTo, text, store: option("--store") });
  const mode = env.FAKE_HKCLI_SEND ?? "ok";
  if (mode === "not_found_reply" && replyTo) {
    fail("not_found", `message ${replyTo} not found in this chat locally; run 'hkcli sync' first`);
  }
  if (mode === "relay_error") {
    fail("relay_error", "Hike did not confirm delivery after 3 attempts; the message may or may not have been sent");
  }
  if (mode === "relay_unavailable") {
    fail("relay_unavailable", "another hkcli process owns this store's relay connection and did not take the message in time; nothing was sent");
  }
  if (mode === "auth") {
    fail("auth_required", "not logged in; run: hkcli auth", 3);
  }
  print({
    success: true,
    data: { to, name: "Peer", client_id: "client-1", server_id: "901:1760000000000", ts: 1760000000000 },
    error: null,
  });
  process.exit(0);
}

if (command === "chats list") {
  log({ command, args });
  const chats = JSON.parse(env.FAKE_HKCLI_CHATS ?? "[]").filter(
    (chat) => !args.includes("--direct") || chat.type === "one_to_one",
  );
  print({ success: true, data: chats.slice(0, Number(option("--limit") ?? 50)), error: null });
  process.exit(0);
}

if (command === "messages list") {
  // Like hkcli: the newest --limit messages older than --before, oldest first.
  const all = JSON.parse(env.FAKE_HKCLI_MESSAGES ?? "{}")[option("--chat")] ?? [];
  const before = option("--before") === undefined ? Infinity : Number(option("--before"));
  const page = all
    .filter((row) => row.ts < before)
    .sort((a, b) => b.ts - a.ts)
    .slice(0, Number(option("--limit") ?? 50))
    .reverse();
  log({ command, chat: option("--chat"), before: option("--before") ?? null });
  print({ success: true, data: page, error: null });
  process.exit(0);
}

if (command === "auth status") {
  log({ command, args });
  if (env.FAKE_HKCLI_AUTH_STATUS === "fail") {
    fail("internal", "could not open the store");
  }
  const user = env.FAKE_HKCLI_USER === "none" ? "" : (env.FAKE_HKCLI_USER ?? "1001");
  print({
    success: true,
    data: { logged_in: Boolean(user), user_id: user || null, name: user ? "Me" : null, store: option("--store") },
    error: null,
  });
  process.exit(0);
}

if (command === "auth logout") {
  log({ command, store: option("--store") });
  print({ success: true, data: { logged_out: true, server_logout: "ok" }, error: null });
  process.exit(0);
}

if (command === "sync") {
  const mode = env.FAKE_HKCLI_SYNC ?? "events";
  log({ command, pid: process.pid });
  if (mode === "auth") {
    fail("auth_required", "the saved login no longer works; run: hkcli auth", 3);
  }
  if (mode === "locked") {
    fail("store_locked", "another hkcli process (pid 4242) is already running sync on this store");
  }
  print({ event: "connected" });
  if (mode === "crash") {
    process.exit(1);
  }
  print({ event: "sync", contacts: 0, chats: 1, chats_fetched: 1, messages: 0, receipts: 0 });
  // A fixed old timestamp by default; "now" makes the DM fresh enough to be answered.
  const ts = env.FAKE_HKCLI_SYNC_TS === "now" ? Date.now() : 1760000060000;
  const message = JSON.stringify({
    event: "message",
    chat: "2002",
    chat_name: "Asha",
    chat_type: "one_to_one",
    id: `40:${ts}`,
    server_id: `40:${ts}`,
    client_id: "c-40",
    sender: "2002",
    sender_name: "Asha",
    from_me: false,
    ts,
    kind: "text",
    text: "héllo 👋",
    file_name: null,
    reply_to: null,
    mentions: [],
    mentions_me: false,
  });
  const bytes = Buffer.from(`${message}\nnot json\n`, "utf8");
  // Split inside a multi-byte character and inside the line.
  const cut = bytes.indexOf(Buffer.from("👋", "utf8")) + 2;
  process.stdout.write(bytes.subarray(0, cut));
  setTimeout(() => process.stdout.write(bytes.subarray(cut)), 20);
  if (mode === "ignore-sigint") {
    process.on("SIGINT", () => log({ event: "sigint-ignored" }));
  } else {
    process.on("SIGINT", () => {
      print({ event: "stopped", reason: "interrupted" });
      process.exit(0);
    });
  }
  setInterval(() => {}, 1_000);
} else if (!args.includes("--version")) {
  process.stderr.write(`usage: fake hkcli does not know "${command}"\n`);
  process.exit(2);
}
