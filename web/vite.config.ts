import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

const HARNESS = "http://127.0.0.1:7331";

// The Go binary embeds the built assets, so the output lands outside this
// folder and keeps a flat assets directory. In development the dev server
// forwards /api to the harness instead, since the two run on different ports.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  server: {
    proxy: {
      "/api": {
        target: HARNESS,
        ws: true,
        headers: {
          // The harness refuses an Origin that is not its own listen address,
          // and identity encoding keeps an event stream from being held in a
          // compression buffer until the turn ends.
          origin: HARNESS,
          "accept-encoding": "identity",
        },
      },
    },
  },
  build: {
    outDir: "../internal/web/dist",
    assetsDir: "assets",
    emptyOutDir: true,
  },
});
