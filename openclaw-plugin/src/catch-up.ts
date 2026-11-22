// hkcli's catch-up after (re)connecting stores missed messages but does not print them as
// events, so direct messages that may not have been handled are read back from the store.
import type { HkcliCommand } from "./hkcli.ts";
import { expectEnvelopeData, hkcliArgs, isRecord, runHkcli } from "./hkcli.ts";
import { parseStoredMessage, type HikeInboundMessage } from "./events.ts";

export type StoredChat = { id: string; type: string; lastTs: number };

export type CatchUpSource = {
  listChats(): Promise<StoredChat[]>;
  /** Newest `pageSize` messages of a chat older than `before` (epoch ms), oldest first. */
  listMessages(chatId: string, before?: number): Promise<unknown[]>;
};

export type CatchUpLimits = { chatLimit: number; maxChats: number; pageSize: number; maxPages: number };

export type CatchUpResult = { messages: HikeInboundMessage[]; truncated: boolean };

export const DEFAULT_CATCH_UP_LIMITS: CatchUpLimits = { chatLimit: 100, maxChats: 50, pageSize: 50, maxPages: 6 };

export function readStoredChats(data: unknown): StoredChat[] {
  if (!Array.isArray(data)) {
    return [];
  }
  return data.filter(isRecord).flatMap((row) => {
    const id = typeof row.id === "string" ? row.id : undefined;
    if (!id) {
      return [];
    }
    return [
      {
        id,
        type: typeof row.type === "string" ? row.type : "",
        lastTs: typeof row.last_ts === "number" ? row.last_ts : 0,
      },
    ];
  });
}

export function createHkcliCatchUpSource(
  command: HkcliCommand,
  storeDir: string,
  options: {
    limits?: CatchUpLimits;
    timeoutMs?: number;
    signal?: AbortSignal;
    env?: NodeJS.ProcessEnv;
  } = {},
): CatchUpSource {
  const limits = options.limits ?? DEFAULT_CATCH_UP_LIMITS;
  const run = (args: string[]) =>
    runHkcli(command, args, { timeoutMs: options.timeoutMs ?? 30_000, signal: options.signal, env: options.env });
  return {
    async listChats() {
      const result = await run(hkcliArgs.chatsList(storeDir, limits.chatLimit));
      return readStoredChats(expectEnvelopeData(result, "hkcli chats list"));
    },
    async listMessages(chatId, before) {
      const result = await run(hkcliArgs.messagesList(storeDir, chatId, limits.pageSize, before));
      const data = expectEnvelopeData(result, "hkcli messages list");
      return Array.isArray(data) ? data : [];
    },
  };
}

function rowTs(row: unknown): number | undefined {
  return isRecord(row) && typeof row.ts === "number" && Number.isFinite(row.ts) ? row.ts : undefined;
}

/**
 * Direct messages from others with ts >= `since`, oldest first, paging back through each chat
 * until `since`. Group chats are skipped: stored rows carry no mention data, so group mention
 * gating can only use live events. `truncated` reports that a limit stopped the read early.
 */
export async function collectMissedDirectMessages(
  source: CatchUpSource,
  since: number,
  limits: CatchUpLimits = DEFAULT_CATCH_UP_LIMITS,
): Promise<CatchUpResult> {
  const listed = await source.listChats();
  const chats = listed
    .filter((chat) => chat.type === "one_to_one" && chat.lastTs >= since)
    .sort((a, b) => b.lastTs - a.lastTs);
  // hkcli lists the newest chats first; a full list that ends inside the window may have left some out.
  let truncated =
    chats.length > limits.maxChats || (listed.length >= limits.chatLimit && (listed.at(-1)?.lastTs ?? 0) >= since);
  const found = new Map<string, HikeInboundMessage>();
  for (const chat of chats.slice(0, limits.maxChats)) {
    let before: number | undefined;
    for (let page = 1; ; page += 1) {
      const rows = await source.listMessages(chat.id, before);
      let oldest = Number.POSITIVE_INFINITY;
      for (const row of rows) {
        oldest = Math.min(oldest, rowTs(row) ?? oldest);
        const message = parseStoredMessage(row, "direct");
        if (message && !message.fromMe && message.ts >= since) {
          found.set(message.id, message);
        }
      }
      if (rows.length < limits.pageSize || oldest < since) {
        break;
      }
      // The next page repeats the oldest timestamp so equal timestamps are never skipped.
      const next = oldest + 1;
      if (page >= limits.maxPages || !Number.isFinite(next) || (before !== undefined && next >= before)) {
        truncated = true;
        break;
      }
      before = next;
    }
  }
  return { messages: [...found.values()].sort((a, b) => a.ts - b.ts), truncated };
}
