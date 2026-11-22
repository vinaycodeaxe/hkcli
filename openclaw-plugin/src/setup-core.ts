// Setup surfaces: `openclaw channels add --channel hike` options and the interactive wizard.
import { parseAllowFromEntries } from "openclaw/plugin-sdk/allow-from";
import { createChannelDmPolicy } from "openclaw/plugin-sdk/channel-dm-policy";
import { defineChannelSetupContract } from "openclaw/plugin-sdk/channel-setup";
import { createDetectedBinaryStatus } from "openclaw/plugin-sdk/setup";
import {
  createCliPathTextInput,
  createPatchedAccountSetupAdapter,
  promptParsedAllowFromForAccount,
  setAccountAllowFromForChannel,
  setSetupChannelEnabled,
  type ChannelSetupAdapter,
  type ChannelSetupWizard,
} from "openclaw/plugin-sdk/setup-runtime";
import { detectBinary } from "openclaw/plugin-sdk/setup-tools";
import { HIKE_CHANNEL_ID, resolveDefaultHikeAccountId, resolveHikeAccount } from "./accounts.ts";
import { loginCommand } from "./auth.ts";
import { HKCLI_GO_INSTALL, HKCLI_RELEASES_URL, MIN_HKCLI_VERSION } from "./hkcli.ts";
import { normalizeHikeUserId } from "./identity.ts";

export const HIKE_DOCS_URL = "https://vinaycodeaxe.github.io/hkcli/openclaw-channel.html";

type HikeSetupInput = { name?: string; cliPath?: string; storeDir?: string };

export function parseHikeAllowFromEntries(raw: string): { entries: string[]; error?: string } {
  return parseAllowFromEntries(raw, (entry) => {
    const id = normalizeHikeUserId(entry);
    return id && /^[\w.+-]+$/.test(id) ? { value: id } : { error: `Invalid Hike user id: ${entry}` };
  });
}

const hikeSetupAdapter: ChannelSetupAdapter = {
  ...createPatchedAccountSetupAdapter({
    channelKey: HIKE_CHANNEL_ID,
    buildPatch: (input) => {
      const { cliPath, storeDir } = input as HikeSetupInput;
      return {
        ...(cliPath?.trim() ? { cliPath: cliPath.trim() } : {}),
        ...(storeDir?.trim() ? { storeDir: storeDir.trim() } : {}),
      };
    },
  }),
  singleAccountKeysToMove: ["cliPath", "storeDir"],
};

export const hikeSetupContract = defineChannelSetupContract({
  fields: {
    cliPath: {
      kind: "string",
      cli: { flags: "--cli-path <path>", description: "hkcli command or path (default: hkcli)" },
    },
    storeDir: {
      kind: "string",
      cli: { flags: "--store-dir <dir>", description: "hkcli store directory for this account" },
    },
  },
  legacyAdapter: hikeSetupAdapter,
});

const hikeDmPolicy = createChannelDmPolicy({
  label: "Hike",
  channel: HIKE_CHANNEL_ID,
  resolveAccount: (cfg, accountId) => resolveHikeAccount({ cfg, accountId }),
  setupSurface: () => hikeSetupAdapter,
  promptAllowFrom: async (params) =>
    promptParsedAllowFromForAccount({
      cfg: params.cfg,
      accountId: params.accountId,
      defaultAccountId: resolveDefaultHikeAccountId(params.cfg),
      prompter: params.prompter,
      noteTitle: "Hike allowlist",
      noteLines: [
        "Allow Hike DMs by Hike user id (numbers, not names).",
        "A sender's id is shown in their pairing message and in `hkcli chats list`.",
        "Multiple entries: comma-separated.",
      ],
      message: "Hike allowFrom (user ids)",
      placeholder: "12345678, 23456789",
      parseEntries: parseHikeAllowFromEntries,
      getExistingAllowFrom: ({ cfg, accountId }) => resolveHikeAccount({ cfg, accountId }).config.allowFrom ?? [],
      applyAllowFrom: ({ cfg, accountId, allowFrom }) =>
        setAccountAllowFromForChannel({
          cfg,
          channel: HIKE_CHANNEL_ID,
          accountId,
          allowFrom,
          setupSurface: hikeSetupAdapter,
        }),
    }),
});

export const hikeSetupWizard: ChannelSetupWizard = {
  channel: HIKE_CHANNEL_ID,
  status: createDetectedBinaryStatus({
    channelLabel: "Hike",
    binaryLabel: "hkcli",
    configuredLabel: "configured",
    unconfiguredLabel: "needs hkcli",
    configuredHint: "hkcli found",
    unconfiguredHint: "hkcli missing",
    configuredScore: 1,
    unconfiguredScore: 0,
    resolveConfigured: async ({ cfg, accountId }) => await detectBinary(resolveHikeAccount({ cfg, accountId }).cliPath),
    resolveBinaryPath: ({ cfg, accountId }) => resolveHikeAccount({ cfg, accountId }).cliPath,
    detectBinary,
  }),
  credentials: [],
  textInputs: [
    createCliPathTextInput({
      inputKey: "cliPath",
      message: "hkcli path",
      resolvePath: ({ cfg, accountId }) => resolveHikeAccount({ cfg, accountId }).config.cliPath ?? "hkcli",
      shouldPrompt: async ({ currentValue }) => !(await detectBinary(currentValue ?? "hkcli")),
      helpTitle: "hkcli",
      helpLines: [
        `The Hike channel runs hkcli ${MIN_HKCLI_VERSION} or newer.`,
        `Install it with: ${HKCLI_GO_INSTALL}`,
        `or download a release from ${HKCLI_RELEASES_URL}`,
      ],
    }),
  ],
  completionNote: {
    title: "Hike next steps",
    lines: [
      "Give the agent its own Hike number (a second phone or SIM), not your personal one.",
      `Link it: ${loginCommand("default")} (scan the QR code from Hike > Settings > Web QR Login).`,
      "Unknown senders get a pairing code; approve with: openclaw pairing approve hike <code>",
      "Groups: add the group id under channels.hike.groups; the agent answers when @mentioned.",
      `Docs: ${HIKE_DOCS_URL}`,
    ],
  },
  dmPolicy: hikeDmPolicy,
  disable: (cfg) => setSetupChannelEnabled(cfg, HIKE_CHANNEL_ID, false),
};
