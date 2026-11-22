// One inbound Hike message through OpenClaw's shared ingress: DM pairing and allowlists,
// group policy and mention gating, then context building and agent dispatch.
import {
  createChannelInboundEnvelopeBuilderAsync,
  logInboundDrop,
  resolveInboundMentionDecision,
} from "openclaw/plugin-sdk/channel-inbound";
import { channelIngressRoutes } from "openclaw/plugin-sdk/channel-ingress-runtime";
import type { OpenClawConfig } from "openclaw/plugin-sdk/channel-core";
import { createChannelPairingController } from "openclaw/plugin-sdk/channel-pairing";
import type { OutboundReplyPayload } from "openclaw/plugin-sdk/reply-payload";
import { resolveAgentRoute } from "openclaw/plugin-sdk/routing";
import type { PluginRuntime } from "openclaw/plugin-sdk/runtime-store";
import {
  GROUP_POLICY_BLOCKED_LABEL,
  resolveAllowlistProviderRuntimeGroupPolicy,
  resolveDefaultGroupPolicy,
  warnMissingProviderGroupPolicyFallbackOnce,
} from "openclaw/plugin-sdk/runtime-group-policy";
import { formatCliCommand } from "openclaw/plugin-sdk/setup-tools";
import { HIKE_CHANNEL_ID, type ResolvedHikeAccount } from "./accounts.ts";
import { inboundBody, type HikeInboundMessage } from "./events.ts";
import { hikeIngressIdentity } from "./identity.ts";
import { chunkHikeText, renderHikeText, sendHikeText } from "./outbound.ts";
import { resolveHikeGroupMatch, resolveHikeGroupRequireMention } from "./policy.ts";

export type HikeLog = {
  info: (message: string) => void;
  warn: (message: string) => void;
  error: (message: string) => void;
  debug?: (message: string) => void;
};

export function hikePairingReply(code: string, senderIdLine: string): string {
  return [
    "OpenClaw: access not configured.",
    "",
    senderIdLine,
    `Pairing code: ${code}`,
    "",
    "Ask the bot owner to approve with:",
    formatCliCommand(`openclaw pairing approve hike ${code}`),
  ].join("\n");
}

function isHttpUrl(value: string | undefined): value is string {
  return typeof value === "string" && /^https?:\/\//i.test(value.trim());
}

function hasEntries(entries: Array<string | number> | undefined): boolean {
  return (entries ?? []).some((entry) => String(entry).trim());
}

export type HikeInboundHandlerParams = {
  cfg: OpenClawConfig;
  account: ResolvedHikeAccount;
  core: PluginRuntime;
  log: HikeLog;
  signal: AbortSignal;
  onInbound?: (at: number) => void;
  onOutbound?: (at: number) => void;
};

export function createHikeInboundHandler(params: HikeInboundHandlerParams) {
  const { cfg, account, core, log } = params;
  const accountId = account.accountId;
  const pairing = createChannelPairingController({ core, channel: HIKE_CHANNEL_ID, accountId });

  const sendText = async (chatId: string, text: string, replyTo?: string) => {
    // Not tied to the account's abort signal: a reply that is ready still goes out during a restart.
    await sendHikeText(account, { to: chatId, text, replyTo });
    params.onOutbound?.(Date.now());
    core.channel.activity.record({ channel: HIKE_CHANNEL_ID, accountId, direction: "outbound" });
  };

  const deliverReply = async (chatId: string, payload: OutboundReplyPayload) => {
    const links = [payload.mediaUrl, ...(payload.mediaUrls ?? [])].filter(isHttpUrl);
    const text = [renderHikeText(payload.text ?? "", cfg, accountId), ...links.map((url) => `Attachment: ${url}`)]
      .filter(Boolean)
      .join("\n\n");
    let replyTo = payload.replyToId;
    for (const chunk of chunkHikeText(text, cfg, accountId)) {
      await sendText(chatId, chunk, replyTo);
      replyTo = undefined;
    }
  };

  return async function handleHikeMessage(message: HikeInboundMessage, commit: () => void = () => {}): Promise<void> {
    const rawBody = inboundBody(message);
    if (!rawBody || !message.senderId) {
      return;
    }
    params.onInbound?.(message.ts);
    core.channel.activity.record({ channel: HIKE_CHANNEL_ID, accountId, direction: "inbound", at: Date.now() });

    const isGroup = message.chatType === "group";
    const dmPolicy = account.config.dmPolicy ?? "pairing";
    const { groupPolicy, providerMissingFallbackApplied } = resolveAllowlistProviderRuntimeGroupPolicy({
      providerConfigPresent: (cfg.channels as Record<string, unknown> | undefined)?.[HIKE_CHANNEL_ID] !== undefined,
      groupPolicy: account.config.groupPolicy,
      defaultGroupPolicy: resolveDefaultGroupPolicy(cfg),
    });
    warnMissingProviderGroupPolicyFallbackOnce({
      providerMissingFallbackApplied,
      providerKey: HIKE_CHANNEL_ID,
      accountId,
      blockedLabel: GROUP_POLICY_BLOCKED_LABEL.group,
      log: (line) => log.info(line),
    });
    const group = resolveHikeGroupMatch(account.config.groups, message.chatId);
    const requireMention = isGroup && resolveHikeGroupRequireMention(account.config.groups, message.chatId);
    const accessGroupPolicy =
      groupPolicy === "open" && (hasEntries(account.config.groupAllowFrom) || group.allowFrom !== undefined)
        ? "allowlist"
        : groupPolicy;
    const allowTextCommands = core.channel.commands.shouldHandleTextCommands({ cfg, surface: HIKE_CHANNEL_ID });
    const hasControlCommand = core.channel.text.hasControlCommand(rawBody, cfg);

    const route = resolveAgentRoute({
      cfg,
      channel: HIKE_CHANNEL_ID,
      accountId,
      peer: { kind: isGroup ? "group" : "direct", id: isGroup ? message.chatId : message.senderId },
    });
    const access = await core.channel.inbound.ingress
      .createResolver({
        channelId: HIKE_CHANNEL_ID,
        accountId,
        identity: hikeIngressIdentity,
        cfg,
        readStoreAllowFrom: async () => await pairing.readAllowFromStore(),
      })
      .message({
        subject: { stableId: message.senderId },
        conversation: { kind: isGroup ? "group" : "direct", id: message.chatId },
        contextBinding: {
          agentId: route.agentId,
          sessionKey: route.sessionKey,
          messageId: message.id,
          inboundEventKind: "user_request",
        },
        route: isGroup
          ? channelIngressRoutes(
              groupPolicy === "allowlist" && {
                id: "hike:group",
                allowed: group.hasConfiguredGroups && group.allowed,
                precedence: 0,
                matchId: "hike-group",
                blockReason: "group_not_allowlisted",
              },
              group.enabled === false && {
                id: "hike:group-enabled",
                enabled: false,
                precedence: 10,
                blockReason: "group_disabled",
              },
              group.allowFrom !== undefined && {
                id: "hike:group-sender",
                precedence: 20,
                senderPolicy: "replace",
                senderAllowFrom: group.allowFrom,
              },
            )
          : [],
        dmPolicy,
        groupPolicy: accessGroupPolicy,
        policy: { groupAllowFromFallbackToAllowFrom: false },
        allowFrom: account.config.allowFrom,
        groupAllowFrom: account.config.groupAllowFrom,
        command: { allowTextCommands, hasControlCommand },
      });

    if (access.ingress.admission === "pairing-required") {
      if (params.signal.aborted) {
        return;
      }
      commit();
      await pairing.issueChallenge({
        senderId: message.senderId,
        senderIdLine: `Your Hike user id: ${message.senderId}`,
        meta: { name: message.senderName },
        buildReplyText: ({ code, senderIdLine }) => hikePairingReply(code, senderIdLine),
        sendPairingReply: async (text) => {
          await sendText(message.chatId, text);
        },
        onReplyError: (error) => {
          log.warn(`hike: pairing reply to ${message.senderId} failed: ${String(error)}`);
        },
      });
      log.info(`hike: pairing required for DM sender ${message.senderId} (dmPolicy=${dmPolicy})`);
      return;
    }
    if (access.ingress.admission !== "dispatch") {
      const reason = isGroup
        ? access.routeAccess.reason ?? `sender not allowed (groupPolicy=${groupPolicy})`
        : `sender not allowed (dmPolicy=${dmPolicy})`;
      logInboundDrop({
        log: (line) => log.info(line),
        channel: HIKE_CHANNEL_ID,
        reason,
        target: isGroup ? message.chatId : message.senderId,
      });
      return;
    }

    const commandAuthorized = access.commandAccess.authorized;
    const mentionFacts = {
      canDetectMention: true,
      wasMentioned: message.mentionsMe,
      hasAnyMention: message.mentions.length > 0,
    };
    const mention = isGroup
      ? resolveInboundMentionDecision({
          facts: mentionFacts,
          policy: { isGroup: true, requireMention, allowTextCommands, hasControlCommand, commandAuthorized },
        })
      : undefined;
    if (mention?.shouldSkip) {
      logInboundDrop({
        log: (line) => log.info(line),
        channel: HIKE_CHANNEL_ID,
        reason: "missing mention",
        target: message.chatId,
        onceKey: JSON.stringify([accountId, message.chatId]),
        hint: `@mention the agent, or set channels.hike.groups["${message.chatId}"].requireMention to false`,
      });
      return;
    }

    const conversationLabel = isGroup
      ? message.chatName ?? message.chatId
      : message.senderName ?? message.senderId;
    const buildEnvelope = await createChannelInboundEnvelopeBuilderAsync({ cfg, route });
    const body = buildEnvelope({
      channel: "Hike",
      from: conversationLabel,
      timestamp: message.ts,
      body: rawBody,
    });
    const groupSystemPrompt = (group.groupConfig?.systemPrompt ?? group.wildcardConfig?.systemPrompt)?.trim();
    const address = `hike:${message.chatId}`;
    const ctxPayload = core.channel.inbound.buildContext({
      channelIngress: access,
      channel: HIKE_CHANNEL_ID,
      accountId: route.accountId,
      messageId: message.id,
      timestamp: message.ts,
      from: isGroup ? address : `hike:${message.senderId}`,
      sender: { id: message.senderId, name: message.senderName },
      conversation: { kind: isGroup ? "group" : "direct", id: message.chatId, label: conversationLabel },
      route: {
        agentId: route.agentId,
        dmScope: route.dmScope,
        accountId: route.accountId,
        routeSessionKey: route.sessionKey,
      },
      reply: { to: address, originatingTo: address },
      message: { body, bodyForAgent: rawBody, rawBody, commandBody: rawBody },
      access: {
        commands: { authorized: commandAuthorized },
        ...(mention
          ? {
              mentions: {
                ...mentionFacts,
                requireMention,
                effectiveWasMentioned: mention.effectiveWasMentioned,
              },
            }
          : {}),
      },
      extra: {
        GroupSubject: isGroup ? message.chatName : undefined,
        GroupSystemPrompt: isGroup && groupSystemPrompt ? groupSystemPrompt : undefined,
      },
    });

    if (params.signal.aborted) {
      return;
    }
    commit();
    await core.channel.inbound.dispatch({
      cfg,
      channel: HIKE_CHANNEL_ID,
      accountId,
      route: { agentId: route.agentId, sessionKey: route.sessionKey },
      ctxPayload,
      delivery: {
        deliver: async (payload) => {
          await deliverReply(message.chatId, payload);
        },
        onError: (error, info) => {
          log.error(`hike: ${info.kind} reply to ${message.chatId} failed: ${String(error)}`);
        },
      },
      replyPipeline: {},
      replyOptions: {
        skillFilter: isGroup ? (group.groupConfig?.skills ?? group.wildcardConfig?.skills) : undefined,
      },
      record: {
        onRecordError: (error) => {
          log.error(`hike: failed updating session metadata: ${String(error)}`);
        },
      },
    });
  };
}
