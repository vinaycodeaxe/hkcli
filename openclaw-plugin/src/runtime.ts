import { createPluginRuntimeStore, type PluginRuntime } from "openclaw/plugin-sdk/runtime-store";

const {
  setRuntime: setHikeRuntime,
  clearRuntime: clearHikeRuntime,
  getRuntime: getHikeRuntime,
  tryGetRuntime: getOptionalHikeRuntime,
} = createPluginRuntimeStore<PluginRuntime>({
  pluginId: "hike",
  errorMessage: "Hike runtime not initialized",
});

export { clearHikeRuntime, getHikeRuntime, getOptionalHikeRuntime, setHikeRuntime };
