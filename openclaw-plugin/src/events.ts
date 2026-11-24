// Parses hkcli `sync --json` events and stored-message records into one inbound shape.
import { isRecord } from "./hkcli.ts";

export type HikeChatType = "direct" | "group";

export type HikeMention = {
  uid?: string;
  phone?: string;
  start: number;
  length: number;
};

export type HikeInboundMessage = {
  id: string;
  serverId?: string;
  clientId?: string;
  chatId: string;
  chatName?: string;
  chatType: HikeChatType;
  senderId: string;
  senderName?: string;
  fromMe: boolean;
  ts: number;
  kind: string;
  text?: string;
  fileName?: string;
  replyTo?: string;
  mentions: HikeMention[];
  mentionsMe: boolean;
  source: "live" | "catch-up";
};

export type HikeSyncEvent =
  | { type: "message"; message: HikeInboundMessage }
  | { type: "connected" }
  | { type: "sync"; messages: number; late: number }
  | { type: "disconnected"; reason?: string }
  | { type: "stopped"; reason?: string }
  | { type: "error"; code: string; message: string }
  | { type: "ignored"; name: string };

function str(value: unknown): string | undefined {
  if (typeof value === "string") {
    return value.trim() ? value : undefined;
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return undefined;
}

function num(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function readMentions(value: unknown): HikeMention[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter(isRecord).map((entry) => ({
    uid: str(entry.uid),
    phone: str(entry.phone),
    start: num(entry.start) ?? 0,
    length: num(entry.length) ?? 0,
  }));
}

function readChatType(value: unknown): HikeChatType | undefined {
  if (value === "group") {
    return "group";
  }
  if (value === "one_to_one" || value === "direct") {
    return "direct";
  }
  return undefined;
}

/** Shared by live events and `messages list` rows, which use the same field names. */
function readMessage(
  record: Record<string, unknown>,
  chatType: HikeChatType | undefined,
  source: HikeInboundMessage["source"],
): HikeInboundMessage | undefined {
  const chatId = str(record.chat);
  const serverId = str(record.server_id);
  const clientId = str(record.client_id);
  const id = str(record.id) ?? serverId ?? clientId;
  const ts = num(record.ts);
  if (!chatId || !id || ts === undefined || !chatType) {
    return undefined;
  }
  const mentions = readMentions(record.mentions);
  return {
    id,
    serverId,
    clientId,
    chatId,
    chatName: str(record.chat_name),
    chatType,
    senderId: str(record.sender) ?? "",
    senderName: str(record.sender_name),
    fromMe: record.from_me === true,
    ts,
    kind: str(record.kind) ?? "text",
    text: typeof record.text === "string" ? record.text : undefined,
    fileName: str(record.file_name),
    replyTo: str(record.reply_to),
    mentions,
    mentionsMe: record.mentions_me === true,
    source,
  };
}

export function parseSyncEvent(value: unknown): HikeSyncEvent | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  if (typeof value.event !== "string") {
    if (value.success === false) {
      const error = isRecord(value.error) ? value.error : {};
      return {
        type: "error",
        code: str(error.code) ?? "error",
        message: str(error.message) ?? "hkcli sync failed",
      };
    }
    return undefined;
  }
  switch (value.event) {
    case "message": {
      const message = readMessage(value, readChatType(value.chat_type), "live");
      return message ? { type: "message", message } : { type: "ignored", name: "message" };
    }
    case "connected":
      return { type: "connected" };
    case "sync":
      return { type: "sync", messages: num(value.messages) ?? 0, late: num(value.late) ?? 0 };
    case "disconnected":
      return { type: "disconnected", reason: str(value.reason) };
    case "stopped":
      return { type: "stopped", reason: str(value.reason) };
    default:
      return { type: "ignored", name: value.event };
  }
}

/** Reads one row of `hkcli --json messages list`; those rows carry no chat type or mentions. */
export function parseStoredMessage(value: unknown, chatType: HikeChatType): HikeInboundMessage | undefined {
  if (!isRecord(value) || value.deleted === true) {
    return undefined;
  }
  return readMessage(value, chatType, "catch-up");
}

const PLACEHOLDER_LABELS: Record<string, string> = {
  image: "photo",
  video: "video",
  audio: "audio",
  document: "file",
  sticker: "sticker",
  gif: "gif",
  location: "location",
  contact: "contact",
};

const SKIPPED_KINDS = new Set(["call", "system", "invite"]);

/** The text the agent sees: the message text, or a short placeholder for media (no download). */
export function inboundBody(message: HikeInboundMessage): string | undefined {
  if (SKIPPED_KINDS.has(message.kind)) {
    return undefined;
  }
  const text = message.text?.trim() ?? "";
  if (message.kind === "text") {
    return text || undefined;
  }
  const label = PLACEHOLDER_LABELS[message.kind] ?? message.kind;
  const placeholder = message.fileName ? `[${label}: ${message.fileName}]` : `[${label}]`;
  return text ? `${placeholder} ${text}` : placeholder;
}
