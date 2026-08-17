import { defineConfig } from "vitest/config";

/* Unit tests for the markdown pipeline's own plugins.
 *
 * NARROW ON PURPOSE. The site is verified by scripts/check-build.mjs against a
 * real build, and that stays the primary harness -- it is what caught this
 * plugin's first version by counting <pre> against Copy buttons. What it cannot
 * do is localise a failure, so the pure predicates in src/lib get direct tests
 * and nothing else here pretends to be covered.
 *
 * `include` is therefore an allow-list rather than the default glob: a coverage
 * report that swept in every .astro component would read as 2% and say nothing,
 * which is the number that makes people stop reading reports. */
export default defineConfig({
  test: {
    include: ["src/lib/**/*.test.{mjs,ts}", "src/scripts/**/*.test.{mjs,ts}"],
    environment: "node",
    coverage: {
      provider: "v8",
      reporter: ["text-summary", "lcov"],
      include: ["src/lib/**/*.mjs", "src/scripts/mermaid-render.ts"],
      // The build guard is the harness for the other two; only the plugin with
      // its own tests is measured here, so the percentage means something.
      exclude: ["src/lib/**/*.test.*"],
    },
  },
});
