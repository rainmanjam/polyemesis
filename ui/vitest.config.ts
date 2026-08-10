import { defineConfig, mergeConfig } from "vitest/config";

import viteConfig from "./vite.config.ts";

/* Unit tests only.
 *
 * `e2e/` is Playwright's, and its specs import @playwright/test — handed to
 * vitest they fail on the import rather than on anything real. Without this
 * scoping, `npm test` reports eight failing files that are not tests of
 * anything vitest was ever meant to run.
 *
 * The vite config is merged rather than restated so the `@/` alias and the
 * TypeScript settings cannot drift from the ones the app is built with. */
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
      exclude: ["e2e/**", "node_modules/**", "dist/**"],
      environment: "node",
    },
  }),
);
