import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, it } from "node:test";
import {
  HkcliSendError,
  NdjsonDecoder,
  hkcliArgs,
  hkcliEnv,
  isMissingReplyTarget,
  isSupportedHkcliVersion,
  parseEnvelope,
  parseHkcliVersion,
  probeHkcliVersion,
  readHkcliUserId,
  sendHkcliText,
} from "../src/hkcli.ts";
import { fakeEnv, fakeHkcli, missingHkcli, scratchDir } from "./helpers.ts";

describe("hkcli version", () => {
  it("parses `hkcli --version` output", () => {
    assert.equal(parseHkcliVersion("hkcli 0.3.0\n"), "0.3.0");
    assert.equal(parseHkcliVersion("hkcli v1.12.4"), "1.12.4");
    assert.equal(parseHkcliVersion("hkcli dev"), undefined);
    assert.equal(parseHkcliVersion(""), undefined);
  });

  it("requires 0.4.0 or newer, comparing numerically", () => {
    assert.equal(isSupportedHkcliVersion("0.3.1"), false);
    assert.equal(isSupportedHkcliVersion("0.4.0"), true);
    assert.equal(isSupportedHkcliVersion("0.10.0"), true);
    assert.equal(isSupportedHkcliVersion("1.0.0"), true);
  });

  it("probes the binary", async () => {
    assert.deepEqual(await probeHkcliVersion(fakeHkcli, { env: fakeEnv({}) }), { ok: true, version: "0.4.0" });
    const old = await probeHkcliVersion(fakeHkcli, { env: fakeEnv({ FAKE_HKCLI_VERSION: "0.3.1" }) });
    assert.equal(old.ok, false);
    assert.equal(!old.ok && old.reason, "too-old");
    assert.match(!old.ok ? old.message : "", /go install github\.com\/vinaycodeaxe\/hkcli\/cmd\/hkcli@latest/);
    const missing = await probeHkcliVersion(missingHkcli);
    assert.equal(!missing.ok && missing.reason, "missing");
  });
});

describe("hkcli arguments", () => {
  it("puts the store and --json before the command", () => {
    assert.deepEqual(hkcliArgs.sync("/s"), ["--store", "/s", "--json", "sync"]);
    assert.deepEqual(hkcliArgs.sendText("/s", "2002"), [
      "--store", "/s", "--json", "send", "text", "--to", "2002", "--message", "-",
    ]);
    assert.deepEqual(hkcliArgs.sendText("/s", "g1", "40:17").slice(-2), ["--reply-to", "40:17"]);
    assert.deepEqual(hkcliArgs.authStatus("/s", true), ["--store", "/s", "--json", "auth", "status", "--check"]);
  });

  it("never passes the user's store or read-only switch to children", () => {
    const env = hkcliEnv({ PATH: "x", HKCLI_STORE: "/home/me/.hkcli", HKCLI_READONLY: "1", HKCLI_API_BASE: "http://fake" });
    assert.deepEqual(env, { PATH: "x", HKCLI_API_BASE: "http://fake" });
  });
});

describe("readHkcliUserId", () => {
  it("reads the linked Hike user from the local auth status", async () => {
    const scratch = scratchDir();
    try {
      const log = path.join(scratch.dir, "calls.ndjson");
      assert.equal(await readHkcliUserId(fakeHkcli, "s", { env: fakeEnv({ FAKE_HKCLI_LOG: log, FAKE_HKCLI_USER: "2009" }) }), "2009");
      const [call] = readFileSync(log, "utf8")
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line));
      // Local only: `--check` would ask Hike.
      assert.deepEqual(call.args, ["--store", "s", "--json", "auth", "status"]);
    } finally {
      scratch.cleanup();
    }
  });

  it("fails when no login is saved or hkcli cannot answer", async () => {
    await assert.rejects(readHkcliUserId(fakeHkcli, "s", { env: fakeEnv({ FAKE_HKCLI_USER: "none" }) }), /no Hike login/);
    await assert.rejects(
      readHkcliUserId(fakeHkcli, "s", { env: fakeEnv({ FAKE_HKCLI_AUTH_STATUS: "fail" }) }),
      /could not open the store/,
    );
  });
});

describe("NdjsonDecoder", () => {
  function collect() {
    const values: unknown[] = [];
    const malformed: number[] = [];
    const decoder = new NdjsonDecoder((value) => values.push(value), (bytes) => malformed.push(bytes), 64);
    return { values, malformed, decoder };
  }

  it("joins UTF-8 characters and lines split across chunks", () => {
    const { values, decoder } = collect();
    const bytes = Buffer.from('{"text":"héllo 👋"}\n{"n":2}\n', "utf8");
    for (let i = 0; i < bytes.length; i += 1) {
      decoder.push(bytes.subarray(i, i + 1));
    }
    assert.deepEqual(values, [{ text: "héllo 👋" }, { n: 2 }]);
  });

  it("handles several lines per chunk, CRLF, blank lines and a final line without newline", () => {
    const { values, decoder } = collect();
    decoder.push('{"a":1}\r\n\n{"b":2}\n{"c"');
    decoder.push(":3}");
    assert.deepEqual(values, [{ a: 1 }, { b: 2 }]);
    decoder.end();
    assert.deepEqual(values, [{ a: 1 }, { b: 2 }, { c: 3 }]);
  });

  it("skips malformed and oversized lines and keeps going", () => {
    const { values, malformed, decoder } = collect();
    decoder.push('not json\n{"ok":true}\n');
    decoder.push(`${"x".repeat(100)}`);
    decoder.push('\n{"after":1}\n');
    assert.deepEqual(values, [{ ok: true }, { after: 1 }]);
    assert.equal(malformed.length, 2);
    assert.equal(malformed[0], 8);
  });

  it("applies the line limit to complete lines too, and reports a long line once", () => {
    const { values, malformed, decoder } = collect();
    const big = JSON.stringify({ text: "y".repeat(100) });
    decoder.push(`${big}\n{"next":1}\n`);
    assert.deepEqual(values, [{ next: 1 }]);
    assert.deepEqual(malformed, [big.length]);

    decoder.push(`{"text":"${"z".repeat(70)}`);
    decoder.push(`${"z".repeat(70)}`);
    decoder.push('"}\n{"after":2}\n');
    assert.deepEqual(values, [{ next: 1 }, { after: 2 }]);
    assert.equal(malformed.length, 2);
  });
});

describe("envelopes", () => {
  it("reads the last JSON line", () => {
    assert.deepEqual(parseEnvelope('noise\n{"success":true,"data":{"x":1},"error":null}\n'), {
      success: true,
      data: { x: 1 },
    });
    assert.deepEqual(parseEnvelope('{"success":false,"data":null,"error":{"code":"store_locked","message":"busy"}}'), {
      success: false,
      error: { code: "store_locked", message: "busy" },
    });
    assert.equal(parseEnvelope("usage: hkcli ..."), undefined);
    assert.equal(parseEnvelope(""), undefined);
  });
});

describe("sendHkcliText", () => {
  it("sends the text on stdin and returns the receipt", async () => {
    const { dir, cleanup } = scratchDir();
    try {
      const log = path.join(dir, "log.jsonl");
      const text = "line one\nline two — ✓ 👋";
      const receipt = await sendHkcliText(fakeHkcli, {
        storeDir: path.join(dir, "store"),
        to: "2002",
        text,
        replyTo: "40:1760000060000",
        env: fakeEnv({ FAKE_HKCLI_LOG: log }),
      });
      assert.deepEqual(receipt, { to: "2002", clientId: "client-1", serverId: "901:1760000000000", ts: 1760000000000 });
      const entry = JSON.parse(readFileSync(log, "utf8").trim());
      assert.deepEqual(entry, {
        command: "send text",
        to: "2002",
        replyTo: "40:1760000060000",
        text,
        store: path.join(dir, "store"),
      });
    } finally {
      cleanup();
    }
  });

  it("marks failures that prove nothing was sent", async () => {
    const reply = await sendHkcliText(fakeHkcli, {
      storeDir: "s",
      to: "2002",
      text: "hi",
      replyTo: "missing-id",
      env: fakeEnv({ FAKE_HKCLI_SEND: "not_found_reply" }),
    }).catch((error: unknown) => error);
    assert.ok(reply instanceof HkcliSendError);
    assert.equal(reply.maybeSent, false);
    assert.equal(isMissingReplyTarget(reply), true);

    const missing = await sendHkcliText(missingHkcli, { storeDir: "s", to: "2002", text: "hi" }).catch(
      (error: unknown) => error,
    );
    assert.ok(missing instanceof HkcliSendError);
    assert.equal(missing.code, "hkcli_missing");
    assert.equal(missing.maybeSent, false);

    const aborted = new AbortController();
    aborted.abort();
    const early = await sendHkcliText(fakeHkcli, { storeDir: "s", to: "2002", text: "hi", signal: aborted.signal }).catch(
      (error: unknown) => error,
    );
    assert.ok(early instanceof HkcliSendError);
    assert.equal(early.maybeSent, false);
  });

  it("treats relay failures after dispatch as ambiguous", async () => {
    const error = await sendHkcliText(fakeHkcli, {
      storeDir: "s",
      to: "2002",
      text: "hi",
      env: fakeEnv({ FAKE_HKCLI_SEND: "relay_error" }),
    }).catch((caught: unknown) => caught);
    assert.ok(error instanceof HkcliSendError);
    assert.equal(error.code, "relay_error");
    assert.equal(error.maybeSent, true);
    assert.equal(isMissingReplyTarget(error), false);
  });
});
