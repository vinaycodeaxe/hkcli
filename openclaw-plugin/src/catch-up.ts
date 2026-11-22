// hkcli's catch-up after (re)connecting stores missed messages but does not print them as
// events, so direct messages that arrived while sync was down are read back from the store.
import type { HkcliCommand } from "./hkcli.ts";
import { expectEnvelopeData, hkcliArgs, isRecord, runHkcli } from "./hkcli.ts";
import { parseStoredMessage, type HikeInboundMessage } from "./events.ts";

export type StoredChat = { id: string; type: string; lastTs: number };

export type CatchUpSource = {
  listChats(): Promise<StoredChat[]>;
  listMessages(chatId: string): Promise<unknown[]>;
};

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
    chatLimit?: number;
    messageLimit?: number;
    timeoutMs?: number;
    signal?: AbortSignal;
    env?: NodeJS.ProcessEnv;
  } = {},
): CatchUpSource {
  const run = (args: string[]) =>
    runHkcli(command, args, { timeoutMs: options.timeoutMs ?? 30_000, signal: options.signal, env: options.env });
  return {
    async listChats() {
      const result = await run(hkcliArgs.chatsList(storeDir, options.chatLimit ?? 50));
      return readStoredChats(expectEnvelopeData(result, "hkcli chats list"));
    },
    async listMessages(chatId) {
      const result = await run(hkcliArgs.messagesList(storeDir, chatId, options.messageLimit ?? 30));
      const data = expectEnvelopeData(result, "hkcli messages list");
      return Array.isArray(data) ? data : [];
    },
  };
}

/**
 * Direct messages from others newer than `since`, oldest first. Group chats are skipped:
 * stored rows carry no mention data, so group mention gating can only use live events.
 */
export async function collectMissedDirectMessages(
  source: CatchUpSource,
  since: number,
  maxChats = 20,
): Promise<HikeInboundMessage[]> {
  const chats = (await source.listChats())
    .filter((chat) => chat.type === "one_to_one" && chat.lastTs >= since)
    .sort((a, b) => b.lastTs - a.lastTs)
    .slice(0, maxChats);
  const messages: HikeInboundMessage[] = [];
  for (const chat of chats) {
    for (const row of await source.listMessages(chat.id)) {
      const message = parseStoredMessage(row, "direct");
      if (message && !message.fromMe && message.ts >= since) {
        messages.push(message);
      }
    }
  }
  return messages.sort((a, b) => a.ts - b.ts);
}
