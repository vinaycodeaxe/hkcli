import { createPluginRuntimeStore, type PluginRuntime } from "openclaw/plugin-sdk/runtime-store";

const {
  setRuntime: setHikeRuntime,
  getRuntime: getHikeRuntime,
  tryGetRuntime: getOptionalHikeRuntime,
} = createPluginRuntimeStore<PluginRuntime>({
  pluginId: "hike",
  errorMessage: "Hike runtime not initialized",
});

export { getHikeRuntime, getOptionalHikeRuntime, setHikeRuntime };
