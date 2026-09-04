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
      // NAMED BY SHAPE, NOT ONE FILE AT A TIME. This used to read
      // "src/scripts/mermaid-render.ts", so the day code-copy.ts got its first
      // test the module still produced no lcov entry -- and a source file with
      // no coverage data is not "unmeasured" to SonarCloud, it is UNCOVERED.
      // The test was written, it passed, and the quality gate got worse. An
      // allow-list of individual paths is exactly the thing that goes stale
      // without anyone being told; see TestEveryTestedModuleIsMeasured.
      include: ["src/lib/**/*.{mjs,ts}", "src/scripts/**/*.ts"],
      // The build guard, scripts/check-build.mjs, is still the harness for the
      // .astro components -- those stay out, because a report that swept them
      // in would read 2% and be the number that makes people stop reading
      // reports.
      exclude: ["src/**/*.test.*"],
    },
  },
});
