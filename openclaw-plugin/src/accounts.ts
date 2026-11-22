import { existsSync } from "node:fs";
import path from "node:path";
import { resolveUserPath } from "openclaw/plugin-sdk/account-core";
import { createAccountListHelpers } from "openclaw/plugin-sdk/account-helpers";
import { DEFAULT_ACCOUNT_ID, normalizeAccountId } from "openclaw/plugin-sdk/account-id";
import type { OpenClawConfig } from "openclaw/plugin-sdk/channel-core";
import { resolveStateDir } from "openclaw/plugin-sdk/state-paths";
import type { HkcliCommand } from "./hkcli.ts";
import type { HikeAccountConfig, HikeConfig } from "./config-schema.ts";

export const HIKE_CHANNEL_ID = "hike";
export const DEFAULT_HKCLI_PATH = "hkcli";

export type ResolvedHikeAccount = {
  accountId: string;
  name?: string;
  enabled: boolean;
  configured: boolean;
  cliPath: string;
  storeDir: string;
  config: HikeAccountConfig;
};

function hikeSection(cfg: OpenClawConfig): HikeConfig | undefined {
  return (cfg.channels as Record<string, unknown> | undefined)?.[HIKE_CHANNEL_ID] as HikeConfig | undefined;
}

const helpers = createAccountListHelpers<HikeAccountConfig>(HIKE_CHANNEL_ID, {
  normalizeAccountId,
  omitKeys: ["defaultAccount"],
  hasImplicitDefaultAccount: (cfg) => hasImplicitDefaultHikeAccount(hikeSection(cfg)),
});

export const listHikeAccountIds = helpers.listAccountIds;
export const resolveDefaultHikeAccountId = helpers.resolveDefaultAccountId;

/**
 * Next to named accounts the top-level (default) account still counts once it has its own
 * storeDir or is linked. Other top-level keys are shared defaults for every account, so they
 * cannot tell whether the default account is in use.
 */
export function hasImplicitDefaultHikeAccount(
  section: Pick<HikeConfig, "storeDir"> | undefined,
  defaultStoreDir: string = defaultHikeStoreDir(DEFAULT_ACCOUNT_ID),
): boolean {
  return Boolean(section?.storeDir?.trim()) || isHikeAccountLinked({ storeDir: defaultStoreDir });
}

/** Account ids become directory names; keep them to a safe character set. */
function safeDirName(accountId: string): string {
  return accountId.replace(/[^a-zA-Z0-9._-]+/g, "_") || DEFAULT_ACCOUNT_ID;
}

export function defaultHikeStoreDir(accountId: string, stateDir: string = resolveStateDir()): string {
  return path.join(stateDir, "hike", safeDirName(accountId));
}

function expandCliPath(cliPath: string): string {
  return /[\\/]/.test(cliPath) || cliPath.startsWith("~") ? resolveUserPath(cliPath) : cliPath;
}

export function resolveHikeAccount(params: { cfg: OpenClawConfig; accountId?: string | null }): ResolvedHikeAccount {
  const accountId = normalizeAccountId(params.accountId ?? resolveDefaultHikeAccountId(params.cfg));
  const section = hikeSection(params.cfg);
  const merged = helpers.resolveAccountConfig(params.cfg, accountId);
  // A root-level storeDir belongs to the default account only; named accounts never share it.
  const ownStoreDir =
    accountId === DEFAULT_ACCOUNT_ID
      ? merged.storeDir
      : section?.accounts?.[accountId]?.storeDir ??
        Object.entries(section?.accounts ?? {}).find(([key]) => normalizeAccountId(key) === accountId)?.[1]
          ?.storeDir;
  const storeDir = ownStoreDir?.trim() ? resolveUserPath(ownStoreDir.trim()) : defaultHikeStoreDir(accountId);
  return {
    accountId,
    name: merged.name?.trim() || undefined,
    enabled: section?.enabled !== false && merged.enabled !== false,
    configured: true,
    cliPath: expandCliPath(merged.cliPath?.trim() || DEFAULT_HKCLI_PATH),
    storeDir,
    config: { ...merged, storeDir },
  };
}

export function hkcliCommandFor(account: Pick<ResolvedHikeAccount, "cliPath">): HkcliCommand {
  return { cliPath: account.cliPath };
}

export function hikeSessionFile(account: Pick<ResolvedHikeAccount, "storeDir">): string {
  return path.join(account.storeDir, "session.json");
}

/** Cheap link check: hkcli keeps its login in session.json; the file is never read here. */
export function isHikeAccountLinked(account: Pick<ResolvedHikeAccount, "storeDir">): boolean {
  return existsSync(hikeSessionFile(account));
}
