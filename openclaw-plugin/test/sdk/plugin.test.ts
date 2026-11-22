// Needs the openclaw package (installed in CI): config schema, accounts, plugin shape, outbound.
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { after, before, describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { PlatformMessageNotDispatchedError } from "openclaw/plugin-sdk/error-runtime";
import { listHikeAccountIds, resolveHikeAccount } from "../../src/accounts.ts";
import { collectHikeStatusIssues, hikePlugin } from "../../src/channel.ts";
import { HikeChannelConfigSchema } from "../../src/config-schema.ts";
import { hikePairingReply } from "../../src/inbound.ts";
import entry from "../../src/index.ts";
import { sendHikeText } from "../../src/outbound.ts";
import { resolveHikeGroupMatch, resolveHikeGroupRequireMention } from "../../src/policy.ts";
import setupEntry from "../../src/setup-entry.ts";
import { scratchDir } from "../helpers.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const cfg = (hike: Record<string, unknown>) => ({ channels: { hike } }) as never;

function parse(value: unknown) {
  return HikeChannelConfigSchema.runtime?.safeParse(value) as
    | { success: true; data: Record<string, unknown> }
    | { success: false; issues: unknown[] };
}

describe("config schema", () => {
  it("defaults to DM pairing and group allowlist", () => {
    const result = parse({});
    assert.equal(result.success, true);
    if (result.success) {
      assert.equal(result.data.dmPolicy, "pairing");
      assert.equal(result.data.groupPolicy, "allowlist");
    }
  });

  it("rejects unknown keys and an open DM policy without a wildcard", () => {
    assert.equal(parse({ bogus: true }).success, false);
    assert.equal(parse({ dmPolicy: "open" }).success, false);
    assert.equal(parse({ dmPolicy: "open", allowFrom: ["*"] }).success, true);
  });

  it("accepts the documented multi-account and group shape", () => {
    const result = parse({
      enabled: true,
      cliPath: "/usr/local/bin/hkcli",
      allowFrom: ["12345"],
      groups: { "*": { requireMention: true }, g3003: { requireMention: false, allowFrom: ["*"] } },
      accounts: { work: { storeDir: "~/hike-work", dmPolicy: "allowlist", allowFrom: ["777"] } },
      defaultAccount: "default",
    });
    assert.equal(result.success, true);
  });

  it("matches the manifest's cold-path schema", () => {
    const manifest = JSON.parse(readFileSync(path.join(here, "..", "..", "openclaw.plugin.json"), "utf8"));
    assert.deepEqual(manifest.channelConfigs.hike.schema, HikeChannelConfigSchema.schema);
  });
});

describe("accounts", () => {
  const previous = process.env.OPENCLAW_STATE_DIR;
  let state: ReturnType<typeof scratchDir>;
  before(() => {
    state = scratchDir();
    process.env.OPENCLAW_STATE_DIR = state.dir;
  });
  after(() => {
    if (previous === undefined) {
      delete process.env.OPENCLAW_STATE_DIR;
    } else {
      process.env.OPENCLAW_STATE_DIR = previous;
    }
    state.cleanup();
  });

  it("keeps each account's hkcli store under the OpenClaw state dir", () => {
    const account = resolveHikeAccount({ cfg: cfg({}) });
    assert.equal(account.accountId, "default");
    assert.equal(account.cliPath, "hkcli");
    assert.equal(account.storeDir, path.join(state.dir, "hike", "default"));
    const work = resolveHikeAccount({ cfg: cfg({ storeDir: "/srv/hike", accounts: { work: {} } }), accountId: "work" });
    assert.equal(work.storeDir, path.join(state.dir, "hike", "work"));
    assert.equal(resolveHikeAccount({ cfg: cfg({ storeDir: "/srv/hike" }) }).storeDir, path.resolve("/srv/hike"));
    assert.ok(listHikeAccountIds(cfg({ accounts: { work: {} } })).includes("work"));
  });

  it("reports the account linked once hkcli saved a session", () => {
    const account = resolveHikeAccount({ cfg: cfg({}) });
    assert.equal(hikePlugin.config.isLinked?.(account, cfg({})), "not-linked");
    mkdirSync(account.storeDir, { recursive: true });
    writeFileSync(path.join(account.storeDir, "session.json"), "{}");
    assert.equal(hikePlugin.config.isLinked?.(account, cfg({})), "linked");
  });
});

describe("group policy", () => {
  it("requires a mention by default and lets a specific group override the wildcard", () => {
    const groups = { "*": { requireMention: true, allowFrom: ["1"] }, g1: { requireMention: false, allowFrom: [] } };
    assert.equal(resolveHikeGroupRequireMention(undefined, "g9"), true);
    assert.equal(resolveHikeGroupRequireMention(groups, "g9"), true);
    assert.equal(resolveHikeGroupRequireMention(groups, "g1"), false);
    assert.deepEqual(resolveHikeGroupMatch(groups, "g1").allowFrom, []);
    assert.deepEqual(resolveHikeGroupMatch(groups, "g9").allowFrom, ["1"]);
    assert.equal(resolveHikeGroupMatch({ g1: {} }, "g2").allowed, false);
  });
});

describe("plugin shape", () => {
  it("registers the hike channel with direct and group chats", () => {
    assert.equal(entry.id, "hike");
    assert.equal(entry.channelPlugin, hikePlugin);
    assert.equal(hikePlugin.id, "hike");
    assert.equal(hikePlugin.meta.label, "Hike");
    assert.deepEqual(hikePlugin.capabilities.chatTypes, ["direct", "group"]);
    assert.equal(typeof hikePlugin.gateway?.startAccount, "function");
    assert.equal(typeof hikePlugin.gateway?.logoutAccount, "function");
    assert.equal(typeof hikePlugin.auth?.login, "function");
    assert.equal(typeof hikePlugin.pairing?.notifyApproval, "function");
    assert.equal(hikePlugin.messaging?.normalizeTarget?.("hike:12345"), "12345");
  });

  it("ships a setup entry that can log in without the gateway runtime", () => {
    const plugin = setupEntry.plugin;
    assert.equal(plugin.id, "hike");
    assert.equal(typeof plugin.auth?.login, "function");
    assert.equal(plugin.gateway?.startAccount, undefined);
  });

  it("turns probe and runtime problems into status issues", () => {
    const issues = collectHikeStatusIssues([
      { accountId: "a", probe: { ok: false, problem: { kind: "config", message: "hkcli missing", fix: "install" } } },
      { accountId: "b", linked: false },
      { accountId: "c", lastError: "boom" },
      { accountId: "d", enabled: false, lastError: "ignored" },
    ]);
    assert.deepEqual(
      issues.map((issue) => [issue.accountId, issue.kind]),
      [
        ["a", "config"],
        ["b", "auth"],
        ["c", "runtime"],
      ],
    );
  });

  it("tells unknown senders how the owner approves them", () => {
    const text = hikePairingReply("ABCD2345", "Your Hike user id: 2009");
    assert.match(text, /Pairing code: ABCD2345/);
    assert.match(text, /openclaw pairing approve hike ABCD2345/);
  });
});

describe("outbound", () => {
  let scratch: ReturnType<typeof scratchDir>;
  let account: ReturnType<typeof resolveHikeAccount>;
  let logFile: string;
  before(() => {
    scratch = scratchDir();
    const wrapper = path.join(scratch.dir, "hkcli");
    const fake = path.join(here, "..", "fixtures", "fake-hkcli.mjs");
    writeFileSync(wrapper, `#!/bin/sh\nexec "${process.execPath}" "${fake}" "$@"\n`);
    chmodSync(wrapper, 0o755);
    logFile = path.join(scratch.dir, "calls.ndjson");
    process.env.FAKE_HKCLI_LOG = logFile;
    account = resolveHikeAccount({ cfg: cfg({ cliPath: wrapper, storeDir: path.join(scratch.dir, "store") }) });
  });
  after(() => {
    delete process.env.FAKE_HKCLI_LOG;
    delete process.env.FAKE_HKCLI_SEND;
    scratch.cleanup();
  });
  const calls = () =>
    readFileSync(logFile, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));

  it("returns hkcli's receipt", async () => {
    process.env.FAKE_HKCLI_SEND = "ok";
    const receipt = await sendHikeText(account, { to: "2002", text: "hi" });
    assert.equal(receipt.serverId, "901:1760000000000");
  });

  it("drops a quote hkcli does not know instead of the message", async () => {
    writeFileSync(logFile, "");
    process.env.FAKE_HKCLI_SEND = "not_found_reply";
    await sendHikeText(account, { to: "2002", text: "hi", replyTo: "m-1" });
    assert.deepEqual(
      calls().map((call) => call.replyTo ?? null),
      ["m-1", null],
    );
  });

  it("marks only definite failures as not dispatched", async () => {
    process.env.FAKE_HKCLI_SEND = "auth";
    await assert.rejects(sendHikeText(account, { to: "2002", text: "hi" }), (error) => {
      assert.ok(error instanceof PlatformMessageNotDispatchedError);
      assert.equal(error.retryable, false);
      return true;
    });
    process.env.FAKE_HKCLI_SEND = "relay_error";
    await assert.rejects(sendHikeText(account, { to: "2002", text: "hi" }), (error) => {
      assert.equal(error instanceof PlatformMessageNotDispatchedError, false);
      return true;
    });
  });
});
