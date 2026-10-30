import { defineConfig } from "vitest/config";

// Coverage is reported to SonarCloud as lcov, the same way ui/ reports it.
// Exclusions are files with nothing a unit test can reach, each with its reason:
//   src/plugin.ts  the Stream Deck SDK adapter. Its only logic is forwarding events to
//                  the runtime, which is what the tests cover. It runs only inside
//                  the Stream Deck app, so no test can drive it without the device.
export default defineConfig({
  test: {
    environment: "node",
    coverage: {
      provider: "v8",
      reporter: ["text-summary", "lcov"],
      reportsDirectory: "./coverage",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.d.ts", "src/plugin.ts"],
    },
  },
});
