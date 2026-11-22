import { DEFAULT_ACCOUNT_ID } from "openclaw/plugin-sdk/account-id";
import { createChatChannelPlugin, type ChannelPlugin } from "openclaw/plugin-sdk/channel-core";
import { PAIRING_APPROVED_MESSAGE } from "openclaw/plugin-sdk/channel-status";
import {
  buildProbeChannelStatusSummary,
  createComputedAccountStatusAdapter,
  createDefaultChannelRuntimeState,
  type ChannelAccountSnapshot,
  type ChannelStatusIssue,
} from "openclaw/plugin-sdk/status-helpers";
import { HIKE_CHANNEL_ID, isHikeAccountLinked, resolveHikeAccount, type ResolvedHikeAccount } from "./accounts.ts";
import { loginCommand, probeHikeAccount, type HikeProbe } from "./auth.ts";
import { logoutHikeGatewayAccount, startHikeAccount } from "./gateway.ts";
import { normalizeHikeUserId } from "./identity.ts";
import { hikeMessageAdapter, hikeOutboundAdapter, normalizeHikeTarget, renderHikeText, sendHikeText } from "./outbound.ts";
import { resolveHikeGroupRequireMention, resolveHikeGroupToolPolicy } from "./policy.ts";
import { createHikePluginBase } from "./shared.ts";

function isHikeProbe(value: unknown): value is HikeProbe {
  return typeof value === "object" && value !== null && typeof (value as HikeProbe).ok === "boolean";
}

export function collectHikeStatusIssues(accounts: ChannelAccountSnapshot[]): ChannelStatusIssue[] {
  return accounts.flatMap((snapshot): ChannelStatusIssue[] => {
    if (snapshot.enabled === false) {
      return [];
    }
    const accountId = snapshot.accountId;
    const problem = isHikeProbe(snapshot.probe) ? snapshot.probe.problem : undefined;
    if (problem) {
      return [{ channel: HIKE_CHANNEL_ID, accountId, kind: problem.kind, message: problem.message, fix: problem.fix }];
    }
    if (snapshot.linked === false) {
      return [
        {
          channel: HIKE_CHANNEL_ID,
          accountId,
          kind: "auth",
          message: "Hike account is not linked.",
          fix: loginCommand(accountId),
        },
      ];
    }
    const lastError = typeof snapshot.lastError === "string" ? snapshot.lastError.trim() : "";
    return lastError
      ? [{ channel: HIKE_CHANNEL_ID, accountId, kind: "runtime", message: `Channel error: ${lastError}` }]
      : [];
  });
}

const base = createHikePluginBase();

export const hikePlugin: ChannelPlugin<ResolvedHikeAccount, HikeProbe> = createChatChannelPlugin<
  ResolvedHikeAccount,
  HikeProbe
>({
  base: {
    ...base,
    groups: {
      resolveRequireMention: ({ cfg, accountId, groupId }) =>
        groupId ? resolveHikeGroupRequireMention(resolveHikeAccount({ cfg, accountId }).config.groups, groupId) : true,
      resolveToolPolicy: ({ cfg, accountId, groupId }) =>
        groupId ? resolveHikeGroupToolPolicy(resolveHikeAccount({ cfg, accountId }).config.groups, groupId) : undefined,
    },
    messaging: {
      targetPrefixes: [HIKE_CHANNEL_ID],
      normalizeTarget: (raw) => normalizeHikeTarget(raw) || undefined,
      targetResolver: {
        looksLikeId: (raw) => /^[\w.+-]+$/.test(normalizeHikeTarget(raw)),
        hint: "<Hike user id|group id>",
      },
    },
    message: hikeMessageAdapter,
    status: createComputedAccountStatusAdapter<ResolvedHikeAccount, HikeProbe>({
      defaultRuntime: createDefaultChannelRuntimeState(DEFAULT_ACCOUNT_ID),
      buildChannelSummary: ({ account, snapshot }) => ({
        ...buildProbeChannelStatusSummary(snapshot),
        linked: snapshot.linked ?? null,
        cliPath: account.cliPath,
      }),
      probeAccount: async ({ account, timeoutMs }) => await probeHikeAccount(account, timeoutMs),
      resolveAccountSnapshot: ({ account }) => ({
        accountId: account.accountId,
        name: account.name,
        enabled: account.enabled,
        configured: account.configured,
        extra: { linked: isHikeAccountLinked(account), cliPath: account.cliPath, storeDir: account.storeDir },
      }),
      collectStatusIssues: collectHikeStatusIssues,
    }),
    gateway: {
      startAccount: startHikeAccount,
      logoutAccount: async ({ account }) => await logoutHikeGatewayAccount(account),
    },
  },
  pairing: {
    text: {
      idLabel: "hikeUserId",
      message: PAIRING_APPROVED_MESSAGE,
      normalizeAllowEntry: (entry) => normalizeHikeUserId(entry) ?? "",
      notify: async ({ cfg, id, message, accountId }) => {
        const to = normalizeHikeUserId(id);
        if (!to) {
          throw new Error(`invalid Hike pairing id: ${id}`);
        }
        const account = resolveHikeAccount({ cfg, accountId });
        await sendHikeText(account, { to, text: renderHikeText(message, cfg, account.accountId) });
      },
    },
  },
  outbound: hikeOutboundAdapter,
});
