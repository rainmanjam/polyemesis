import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Runs test/fixtures/*.fixture.ts with the suite's own setup, for test/contract.test.ts's check that the
// schema hook fails a test. Never part of `npm test`.
export default defineConfig({
  test: {
    root: fileURLToPath(new URL("../..", import.meta.url)),
    environment: "node",
    include: ["test/fixtures/*.fixture.ts"],
    setupFiles: ["test/setup.ts"],
    fsModuleCache: false,
  },
});
