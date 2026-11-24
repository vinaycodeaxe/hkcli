import {
  buildChannelAccountSchemaParts,
  buildChannelConfigSchema,
  buildGroupEntrySchema,
  refineChannelDmPolicy,
} from "openclaw/plugin-sdk/channel-config-schema";
import { createChannelConfigUiHints, type ChannelConfigUiHint } from "openclaw/plugin-sdk/channel-core";
import { z } from "zod";

const { accountShape, rootPolicyShape } = buildChannelAccountSchemaParts({
  omit: [
    "capabilities",
    "mentionPatterns",
    "contextVisibility",
    "historyLimit",
    "dmHistoryLimit",
    "dms",
    "streaming",
    "responsePrefix",
    "mediaMaxMb",
    "replyToMode",
  ],
});

/** Per-group settings, keyed by Hike group id or "*". requireMention defaults to true. */
export const HikeGroupSchema = buildGroupEntrySchema({}, { omit: ["toolsBySender"] });

export const HikeAccountSchema = z
  .object({
    ...accountShape,
    cliPath: z.string().optional(),
    storeDir: z.string().optional(),
    groups: z.record(z.string(), HikeGroupSchema.optional()).optional(),
  })
  .strict();

export const HikeConfigSchema = HikeAccountSchema.extend({
  ...rootPolicyShape,
  accounts: z.record(z.string(), HikeAccountSchema.optional()).optional(),
  defaultAccount: z.string().optional(),
}).superRefine((value, ctx) => {
  refineChannelDmPolicy({ channelId: "hike", value, ctx });
  for (const [accountId, account] of Object.entries(value.accounts ?? {})) {
    if (account) {
      refineChannelDmPolicy({ channelId: "hike", value, accountId, ctx });
    }
  }
});

export type HikeAccountConfig = z.input<typeof HikeAccountSchema>;
export type HikeGroupConfig = z.input<typeof HikeGroupSchema>;
export type HikeConfig = z.input<typeof HikeConfigSchema>;

const hikeUiHints = {
  "": {
    label: "Hike",
    help: "Hike Messenger through the hkcli command line tool. The agent uses its own Hike account, linked with: openclaw channels login --channel hike.",
  },
  ...createChannelConfigUiHints({
    channelLabel: "Hike",
    dmPolicy: { channelKey: "hike" },
    configWrites: true,
  }),
  cliPath: {
    label: "hkcli Path",
    help: "Command or path of the hkcli binary (default: hkcli on PATH). Needs hkcli 0.4.0 or newer.",
  },
  storeDir: {
    label: "hkcli Store Directory",
    help: "Where hkcli keeps this account's login and messages (default: <OpenClaw state dir>/hike/<accountId>). Never share it with another account.",
  },
  allowFrom: {
    label: "Hike DM Allowlist",
    help: 'Hike user ids allowed to message the agent directly; "*" allows everyone when dmPolicy is "open".',
  },
  defaultTo: {
    label: "Hike Default Target",
    help: "Hike user or group id used when a send names no target.",
  },
  groupPolicy: {
    label: "Hike Group Policy",
    help: '"allowlist" (default) answers only groups listed in groups; "open" answers any group; "disabled" ignores groups.',
  },
  groupAllowFrom: {
    label: "Hike Group Sender Allowlist",
    help: 'Hike user ids allowed to trigger the agent in groups ("*" for everyone). With groupPolicy "allowlist", a group needs sender entries here or in groups.<id>.allowFrom.',
  },
  groups: {
    label: "Hike Groups",
    help: 'Per-group settings keyed by Hike group id, or "*" for every group: enabled, requireMention (default true), allowFrom (["*"] lets every member trigger the agent).',
  },
} satisfies Record<string, ChannelConfigUiHint>;

export const HikeChannelConfigSchema = buildChannelConfigSchema(HikeConfigSchema, {
  uiHints: hikeUiHints,
});
