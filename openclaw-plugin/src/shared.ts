// Plugin surfaces shared by the full channel and the setup-only entry.
import { describeAccountSnapshot } from "openclaw/plugin-sdk/account-helpers";
import { formatNormalizedAllowFromEntries } from "openclaw/plugin-sdk/allow-from";
import {
  adaptScopedAccountAccessor,
  createScopedChannelConfigAdapter,
} from "openclaw/plugin-sdk/channel-config-helpers";
import type { ChannelPlugin, OpenClawConfig } from "openclaw/plugin-sdk/channel-core";
import { identityEntryAuthenticationClassifier } from "openclaw/plugin-sdk/channel-ingress-runtime";
import { createRestrictSendersChannelSecurity } from "openclaw/plugin-sdk/channel-policy";
import type { RuntimeEnv } from "openclaw/plugin-sdk/runtime-env";
import {
  HIKE_CHANNEL_ID,
  isHikeAccountLinked,
  listHikeAccountIds,
  resolveDefaultHikeAccountId,
  resolveHikeAccount,
  type ResolvedHikeAccount,
} from "./accounts.ts";
import { loginCommand, loginHikeAccount } from "./auth.ts";
import { HikeChannelConfigSchema } from "./config-schema.ts";
import { hikeIngressIdentity, normalizeHikeUserId } from "./identity.ts";
import { HIKE_DOCS_URL, hikeSetupContract, hikeSetupWizard } from "./setup-core.ts";

export const hikeMeta = {
  id: HIKE_CHANNEL_ID,
  label: "Hike",
  selectionLabel: "Hike (via hkcli)",
  detailLabel: "Hike Messenger",
  docsPath: HIKE_DOCS_URL,
  docsLabel: "hike",
  blurb: "Hike Messenger through the hkcli CLI; give the agent its own Hike number and link it with a QR code.",
  markdownCapable: false,
};

const hikeConfigAdapter = createScopedChannelConfigAdapter<ResolvedHikeAccount>({
  sectionKey: HIKE_CHANNEL_ID,
  listAccountIds: listHikeAccountIds,
  resolveAccount: adaptScopedAccountAccessor(resolveHikeAccount),
  defaultAccountId: resolveDefaultHikeAccountId,
  clearBaseFields: ["name", "cliPath", "storeDir", "defaultTo"],
  resolveAllowFrom: (account: ResolvedHikeAccount) => account.config.allowFrom,
  formatAllowFrom: (allowFrom) => formatNormalizedAllowFromEntries({ allowFrom, normalizeEntry: normalizeHikeUserId }),
  resolveDefaultTo: (account: ResolvedHikeAccount) => account.config.defaultTo,
});

export const hikeSecurityAdapter = createRestrictSendersChannelSecurity<ResolvedHikeAccount>({
  channelKey: HIKE_CHANNEL_ID,
  resolveDmPolicy: (account) => account.config.dmPolicy,
  resolveDmAllowFrom: (account) => account.config.allowFrom,
  resolveGroupPolicy: (account) => account.config.groupPolicy,
  surface: "Hike groups",
  openScope: "any member",
  groupPolicyPath: "channels.hike.groupPolicy",
  groupAllowFromPath: "channels.hike.groupAllowFrom",
  mentionGated: true,
  findingTitle: "Hike security warning",
  policyPathSuffix: "dmPolicy",
  normalizeDmEntry: (raw) => normalizeHikeUserId(raw) ?? "",
  classifyEntryAuthentication: identityEntryAuthenticationClassifier(hikeIngressIdentity),
});

function channelConfigured(cfg: OpenClawConfig): boolean {
  const section = (cfg.channels as Record<string, unknown> | undefined)?.[HIKE_CHANNEL_ID];
  return typeof section === "object" && section !== null && Object.keys(section).length > 0;
}

async function login(params: { cfg: OpenClawConfig; accountId?: string | null; runtime: RuntimeEnv }) {
  const account = resolveHikeAccount({ cfg: params.cfg, accountId: params.accountId });
  await loginHikeAccount(account, params.runtime);
  if (!channelConfigured(params.cfg)) {
    params.runtime.log(
      "Turn the Hike channel on with: openclaw config set channels.hike.enabled true, then restart the gateway: openclaw gateway restart",
    );
  } else if (!account.enabled) {
    params.runtime.log(`Hike account "${account.accountId}" is disabled in config (enabled: false); the gateway will not start it.`);
  }
}

/** Everything both entries share; the full plugin adds the gateway, outbound and status surfaces. */
export function createHikePluginBase() {
  return {
    id: HIKE_CHANNEL_ID,
    meta: hikeMeta,
    capabilities: { chatTypes: ["direct", "group"] },
    reload: { configPrefixes: ["channels.hike"] },
    configSchema: HikeChannelConfigSchema,
    config: {
      ...hikeConfigAdapter,
      isConfigured: (account) => account.configured,
      isLinked: (account) => (isHikeAccountLinked(account) ? "linked" : "not-linked"),
      unlinkedReason: (account) => `not linked; run ${loginCommand(account.accountId)}`,
      describeAccount: (account) =>
        describeAccountSnapshot({
          account,
          configured: account.configured,
          extra: { linked: isHikeAccountLinked(account), cliPath: account.cliPath, storeDir: account.storeDir },
        }),
    },
    setupContract: hikeSetupContract,
    setupWizard: hikeSetupWizard,
    security: hikeSecurityAdapter,
    auth: { login },
  } satisfies Partial<ChannelPlugin<ResolvedHikeAccount>>;
}
