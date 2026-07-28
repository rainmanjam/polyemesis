import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
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
