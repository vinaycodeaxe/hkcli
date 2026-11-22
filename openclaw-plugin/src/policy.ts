// Group policy lookups shared by the inbound path and the channel's groups adapter.
import {
  resolveScopeKeyCaseInsensitive,
  resolveScopeRequireMention,
  resolveScopeToolsPolicy,
  type GroupToolPolicyConfig,
  type ScopeTree,
} from "openclaw/plugin-sdk/channel-policy";
import type { HikeGroupConfig } from "./config-schema.ts";

type HikeGroups = Record<string, HikeGroupConfig | undefined> | undefined;

function scope(groups: HikeGroups, groupId: string) {
  const { "*": wildcard, ...named } = groups ?? {};
  const project = (entry: HikeGroupConfig | undefined) => ({
    requireMention: entry?.requireMention,
    tools: entry?.tools,
  });
  const tree: ScopeTree = {
    defaults: wildcard ? project(wildcard) : undefined,
    scopes: Object.fromEntries(Object.entries(named).map(([key, entry]) => [key, project(entry)])),
  };
  const key = resolveScopeKeyCaseInsensitive(tree, groupId);
  return { tree, key, wildcard };
}

export function resolveHikeGroupMatch(groups: HikeGroups, groupId: string) {
  const { key, wildcard } = scope(groups, groupId);
  const groupConfig = key ? groups?.[key] : undefined;
  return {
    allowed: Boolean(groupConfig ?? wildcard),
    groupConfig,
    wildcardConfig: wildcard,
    hasConfiguredGroups: Object.keys(groups ?? {}).length > 0,
    /** The specific group's setting wins over the "*" entry. */
    enabled: groupConfig?.enabled ?? wildcard?.enabled,
    allowFrom: groupConfig?.allowFrom ?? wildcard?.allowFrom,
  };
}

/** Defaults to true: in groups the agent answers only when @mentioned. */
export function resolveHikeGroupRequireMention(groups: HikeGroups, groupId: string): boolean {
  const { tree, key } = scope(groups, groupId);
  return resolveScopeRequireMention({ tree, path: key ? [key] : [] });
}

export function resolveHikeGroupToolPolicy(groups: HikeGroups, groupId: string): GroupToolPolicyConfig | undefined {
  const { tree, key } = scope(groups, groupId);
  return resolveScopeToolsPolicy({ tree, path: key ? [key] : [] });
}
