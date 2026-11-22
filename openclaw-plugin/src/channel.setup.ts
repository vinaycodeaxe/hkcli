import type { ChannelPlugin } from "openclaw/plugin-sdk/channel-core";
import type { ResolvedHikeAccount } from "./accounts.ts";
import { logoutHikeAccount } from "./auth.ts";
import { createHikePluginBase } from "./shared.ts";

/** Setup-safe plugin: config, setup and login/logout without the gateway runtime. */
export const hikeSetupPlugin: ChannelPlugin<ResolvedHikeAccount> = {
  ...createHikePluginBase(),
  gateway: {
    logoutAccount: async ({ account }) => await logoutHikeAccount(account),
  },
};
