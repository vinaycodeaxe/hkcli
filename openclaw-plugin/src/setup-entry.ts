import { defineSetupPluginEntry } from "openclaw/plugin-sdk/channel-core";
import { hikeSetupPlugin } from "./channel.setup.ts";

export default defineSetupPluginEntry(hikeSetupPlugin);
