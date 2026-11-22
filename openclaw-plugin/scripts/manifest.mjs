// Keeps openclaw.plugin.json#channelConfigs in sync with the runtime config schema.
// Usage (after `npm run build`): node scripts/manifest.mjs --write | --check
import { readFileSync, writeFileSync } from "node:fs";

const mode = process.argv[2];
if (mode !== "--write" && mode !== "--check") {
  console.error("usage: node scripts/manifest.mjs --write | --check");
  process.exit(2);
}

const manifestUrl = new URL("../openclaw.plugin.json", import.meta.url);
const { HikeChannelConfigSchema } = await import("../dist/config-schema.js");

const current = readFileSync(manifestUrl, "utf8");
const manifest = JSON.parse(current);
manifest.channelConfigs = {
  hike: {
    label: "Hike",
    description: "Hike Messenger through the hkcli command line tool.",
    schema: HikeChannelConfigSchema.schema,
    ...(HikeChannelConfigSchema.uiHints ? { uiHints: HikeChannelConfigSchema.uiHints } : {}),
  },
};
const next = `${JSON.stringify(manifest, null, 2)}\n`;

if (mode === "--write") {
  writeFileSync(manifestUrl, next);
  console.log("openclaw.plugin.json updated");
} else if (next !== current) {
  console.error("openclaw.plugin.json is out of date; run: npm run build && npm run manifest:write");
  process.exit(1);
} else {
  console.log("openclaw.plugin.json is up to date");
}
