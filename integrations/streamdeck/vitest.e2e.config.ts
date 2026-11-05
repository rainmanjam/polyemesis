import { defineConfig } from "vitest/config";

// The end-to-end run: the plugin's Runtime against a real polyemesis over real HTTP. It needs a running server
// that scripts/streamdeck-e2e.sh starts and seeds, so it is not part of `npm test`; run the script instead.
// One file, in order: each flow starts from the state the previous one left.
export default defineConfig({
  test: {
    environment: "node",
    include: ["test/e2e/**/*.e2e.test.ts"],
    fileParallelism: false,
    sequence: { concurrent: false },
    testTimeout: 120_000,
    hookTimeout: 120_000,
    // One cold run, so nothing to gain from the module cache, and it lives at the workspace root that Vite
    // searches up for: outside this checkout on a machine with a workspace marker above it, shared with
    // whatever else runs there.
    fsModuleCache: false,
  },
});
