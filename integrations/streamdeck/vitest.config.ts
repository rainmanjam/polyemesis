import { defineConfig } from "vitest/config";

// Coverage is reported to SonarCloud as lcov, the same way ui/ reports it.
// Exclusions are files with nothing a unit test can reach, each with its reason:
//   src/plugin.ts  the Stream Deck SDK adapter. Its only logic is forwarding events to
//                  the runtime, which is what the tests cover. It runs only inside
//                  the Stream Deck app, so no test can drive it without the device.
//
// test/setup.ts holds every fake answer to the server's recorded schema (test/contract/). test/e2e/ runs
// against a real server and has its own config, vitest.e2e.config.ts: `npm run test:e2e`.
export default defineConfig({
  test: {
    environment: "node",
    setupFiles: ["test/setup.ts"],
    exclude: ["**/node_modules/**", "test/e2e/**"],
    coverage: {
      provider: "v8",
      reporter: ["text-summary", "lcov"],
      reportsDirectory: "./coverage",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.d.ts", "src/plugin.ts"],
    },
  },
});
