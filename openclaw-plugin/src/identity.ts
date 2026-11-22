import { defineStableChannelIngressIdentity } from "openclaw/plugin-sdk/channel-ingress-runtime";

/** Hike user ids are stable numeric ids; entries may carry a "hike:" prefix. */
export function normalizeHikeUserId(value: string): string | null {
  const id = value.trim().replace(/^hike:/i, "").trim();
  return id ? id : null;
}

export const hikeIngressIdentity = defineStableChannelIngressIdentity({
  key: "hike-user-id",
  normalize: normalizeHikeUserId,
  // Hike's relay binds the sender id to the publishing session; hkcli reports it as received.
  authentication: "asserted",
  sensitivity: "pii",
  isWildcardEntry: (entry) => entry.trim() === "*",
  entryIdPrefix: "hike-entry",
});
