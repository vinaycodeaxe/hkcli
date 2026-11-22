import type { ChannelOutboundAdapter } from "openclaw/plugin-sdk/channel-contract";
import type { OpenClawConfig } from "openclaw/plugin-sdk/channel-core";
import {
  createChannelMessageAdapterFromOutbound,
  createMessageReceiptFromOutboundResults,
} from "openclaw/plugin-sdk/channel-outbound";
import { attachChannelToResult } from "openclaw/plugin-sdk/channel-send-result";
import { PlatformMessageNotDispatchedError } from "openclaw/plugin-sdk/error-runtime";
import {
  chunkTextForOutbound,
  sanitizeAssistantVisibleText,
  stripMarkdown,
} from "openclaw/plugin-sdk/text-chunking";
import { HIKE_CHANNEL_ID, hkcliCommandFor, resolveHikeAccount, type ResolvedHikeAccount } from "./accounts.ts";
import { HkcliSendError, isMissingReplyTarget, sendHkcliText, type HkcliSendReceipt } from "./hkcli.ts";
import { getOptionalHikeRuntime } from "./runtime.ts";

export const HIKE_TEXT_CHUNK_LIMIT = 4000;

/** Send failures that will not fix themselves on retry. */
const PERMANENT_SEND_CODES = new Set([
  "auth_required",
  "hkcli_missing",
  "invalid_input",
  "not_found",
  "ambiguous",
  "read_only",
  "usage",
]);

export function normalizeHikeTarget(raw: string | undefined | null): string {
  return (raw ?? "").trim().replace(/^hike:/i, "").trim();
}

/** Hike shows plain text: drop assistant scaffolding and Markdown syntax. */
export function renderHikeText(text: string, cfg?: OpenClawConfig, accountId?: string | null): string {
  let rendered = sanitizeAssistantVisibleText(text);
  const runtime = getOptionalHikeRuntime();
  if (cfg && runtime) {
    const tableMode = runtime.channel.text.resolveMarkdownTableMode({
      cfg,
      channel: HIKE_CHANNEL_ID,
      accountId: accountId ?? undefined,
    });
    rendered = runtime.channel.text.convertMarkdownTables(rendered, tableMode);
  }
  return stripMarkdown(rendered).trim();
}

export function chunkHikeText(text: string, cfg?: OpenClawConfig, accountId?: string | null): string[] {
  const runtime = getOptionalHikeRuntime();
  const limit =
    cfg && runtime
      ? runtime.channel.text.resolveTextChunkLimit(cfg, HIKE_CHANNEL_ID, accountId ?? undefined, {
          fallbackLimit: HIKE_TEXT_CHUNK_LIMIT,
        })
      : HIKE_TEXT_CHUNK_LIMIT;
  return chunkTextForOutbound(text, limit).filter((chunk) => chunk.trim());
}

function toDeliveryError(error: unknown): Error {
  if (error instanceof HkcliSendError && !error.maybeSent) {
    return new PlatformMessageNotDispatchedError(error.message, {
      cause: error,
      retryable: !PERMANENT_SEND_CODES.has(error.code),
    });
  }
  // The message may already be on Hike: report the failure without claiming it was not sent.
  return error instanceof Error ? error : new Error(String(error));
}

/**
 * Sends one already-rendered text message. A quoted message hkcli does not know locally only
 * drops the quote (nothing was sent), never the message.
 */
export async function sendHikeText(
  account: ResolvedHikeAccount,
  params: { to: string; text: string; replyTo?: string; signal?: AbortSignal },
): Promise<HkcliSendReceipt> {
  const send = (replyTo?: string) =>
    sendHkcliText(hkcliCommandFor(account), {
      storeDir: account.storeDir,
      to: params.to,
      text: params.text,
      replyTo,
      signal: params.signal,
    });
  try {
    try {
      return await send(params.replyTo);
    } catch (error) {
      if (params.replyTo && isMissingReplyTarget(error)) {
        return await send(undefined);
      }
      throw error;
    }
  } catch (error) {
    throw toDeliveryError(error);
  }
}

export function receiptMessageId(receipt: HkcliSendReceipt): string {
  return receipt.serverId ?? receipt.clientId ?? "";
}

export const hikeOutboundAdapter = {
  deliveryMode: "direct",
  textChunkLimit: HIKE_TEXT_CHUNK_LIMIT,
  chunker: chunkTextForOutbound,
  chunkerMode: "text",
  sanitizeText: ({ text }) => sanitizeAssistantVisibleText(text),
  deliveryCapabilities: {
    durableFinal: {
      text: true,
      replyTo: true,
      messageSendingHooks: true,
    },
  },
  resolveTarget: ({ to }) => {
    const target = normalizeHikeTarget(to);
    return target
      ? { ok: true, to: target }
      : { ok: false, error: new Error("Hike needs a target: a Hike user id or group id.") };
  },
  sendText: async (ctx) => {
    const account = resolveHikeAccount({ cfg: ctx.cfg, accountId: ctx.accountId });
    const to = normalizeHikeTarget(ctx.to);
    const text = renderHikeText(ctx.text ?? "", ctx.cfg, account.accountId);
    if (!to || !text) {
      throw new PlatformMessageNotDispatchedError("Hike send needs a target and non-empty text.", {
        cause: new Error("empty send"),
        retryable: false,
      });
    }
    if (ctx.signal?.aborted) {
      throw new PlatformMessageNotDispatchedError("Hike send was cancelled before dispatch.", {
        cause: ctx.signal.reason,
      });
    }
    await ctx.onPlatformSendDispatch?.();
    ctx.assertDirectAdapterHandoff?.();
    const replyTo = ctx.replyToId ? String(ctx.replyToId) : undefined;
    const receipt = await sendHikeText(account, { to, text, replyTo, signal: ctx.signal });
    getOptionalHikeRuntime()?.channel.activity.record({
      channel: HIKE_CHANNEL_ID,
      accountId: account.accountId,
      direction: "outbound",
    });
    const messageId = receiptMessageId(receipt);
    return attachChannelToResult(HIKE_CHANNEL_ID, {
      messageId,
      target: { kind: "chat" as const, id: receipt.to },
      ...(receipt.ts !== undefined ? { timestamp: receipt.ts } : {}),
      receipt: createMessageReceiptFromOutboundResults({
        results: [
          {
            channel: HIKE_CHANNEL_ID,
            messageId,
            chatId: receipt.to,
            ...(receipt.ts !== undefined ? { timestamp: receipt.ts } : {}),
          },
        ],
        kind: "text",
        ...(replyTo ? { replyToId: replyTo } : {}),
      }),
    });
  },
} satisfies ChannelOutboundAdapter;

export const hikeMessageAdapter = createChannelMessageAdapterFromOutbound({
  id: HIKE_CHANNEL_ID,
  outbound: hikeOutboundAdapter,
});
