import { defineChannelPluginEntry } from "openclaw/plugin-sdk/channel-core";
import { hikePlugin } from "./channel.ts";
import { setHikeRuntime } from "./runtime.ts";

export { hikePlugin } from "./channel.ts";

export default defineChannelPluginEntry({
  id: "hike",
  name: "Hike",
  description: "Hike Messenger channel for OpenClaw, powered by the hkcli command line tool.",
  plugin: hikePlugin,
  setRuntime: setHikeRuntime,
});
