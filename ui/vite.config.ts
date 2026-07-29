import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import fs from "node:fs";

// dist/.gitkeep is the only file in internal/web/dist that is COMMITTED, and
// emptyOutDir wipes the directory on every build. Losing it breaks `go build`
// in a clean checkout with "pattern all:dist: no matching files found" -- and
// ONLY in a clean checkout, because the machine that deleted it still has a
// populated dist/ and never sees the failure.
//
// That has now broken CI three separate times in this repository, each time
// discovered on a runner rather than by the person who deleted it, so it is
// fixed in the build rather than in anyone's memory.
const KEEP = "internal/web/dist/.gitkeep";
const FALLBACK_KEEP = `This file exists so \`go build ./...\` works in a clean checkout.

//go:embed all:dist in web.go needs the directory to contain at least one
file; without it the build fails with "pattern all:dist: no matching files
found" -- and only in a fresh clone, because any machine that has run
\`make build\` has a populated dist/ and never sees it.

Restored automatically by the keep-gitkeep plugin in ui/vite.config.ts.
`;
function keepGitkeep(outDir: string) {
  const source = path.resolve(__dirname, "..", KEEP);
  const saved = fs.existsSync(source) ? fs.readFileSync(source) : null;
  return {
    name: "polyemesis-keep-gitkeep",
    // closeBundle runs after the output directory has been emptied and
    // rewritten, which is the only point at which restoring it sticks.
    closeBundle() {
      const target = path.join(outDir, ".gitkeep");
      if (fs.existsSync(target)) return;
      // The saved copy when there was one, and a short explanation when there
      // was not. Unconditional on purpose: a build that starts with the file
      // ALREADY deleted is exactly the state this is meant to repair, so a
      // guard that only preserves an existing file would do nothing in the one
      // case that matters.
      fs.writeFileSync(target, saved ?? FALLBACK_KEEP);
    },
  };
}

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    keepGitkeep(path.resolve(__dirname, "../internal/web/dist")),
  ],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  build: {
    // Straight into the Go package that embeds it, so `make build` is one step
    // and there is no copy phase to forget.
    outDir: path.resolve(__dirname, "../internal/web/dist"),
    emptyOutDir: true,
    // The app is one bundle apart from the two routes that drag in a chart
    // library and an HLS player. Those are big enough to be worth a second
    // round trip; splitting anything finer would not be.
    chunkSizeWarningLimit: 1000,
  },
  server: {
    port: 5173,
    // `npm run dev` talks to a polyemesis running on :8080, so the SPA can be
    // hot-reloaded against a real backend and a real stream.
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: true, ws: true },
      "/hls": { target: "http://127.0.0.1:8080", changeOrigin: true },
    },
  },
});
