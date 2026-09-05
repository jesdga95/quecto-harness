import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

const HARNESS = "http://127.0.0.1:7331";

// Each generated phosphor icon carries all six weights and picks one at
// runtime, so five sets of path data ride along that nothing can reach. The
// interface asks for no weight anywhere, so the branch is cut to the regular
// arm before the svelte compiler sees the file.
const REGULAR = /\{#if weight === "bold"\}[\s\S]*?\{:else if weight === "regular"\}([\s\S]*?)\{:else if weight === "thin"\}[\s\S]*?\{\/if\}/;

function oneIconWeight(): Plugin {
  return {
    name: "one-icon-weight",
    enforce: "pre",
    transform(code, id) {
      if (!id.includes("phosphor-svelte/lib/") || !id.endsWith(".svelte")) return null;
      // IconContext carries no paths and is the one file in there with no
      // branch to cut. Anything else that names a weight must match, so a
      // change to the generated shape fails the build instead of quietly
      // putting all six back.
      if (!code.includes("weight ===")) return null;
      const cut = code.replace(REGULAR, "$1");
      if (cut === code) {
        this.error(`${id} does not have the weight branch this build expects`);
      }
      return cut;
    },
  };
}

// KaTeX declares each face three times over, and the woff and ttf sources are
// only ever reached by a browser too old to run this interface. Dropping the
// sources leaves the files unreferenced, and an unreferenced font is not worth
// embedding, so it is dropped from the output too.
function woff2Only(): Plugin {
  return {
    name: "woff2-only",
    generateBundle(_options, bundle) {
      let stripped = false;
      for (const asset of Object.values(bundle)) {
        if (asset.type !== "asset" || !asset.fileName.endsWith(".css")) continue;
        const css = String(asset.source);
        const cut = css.replace(/,url\([^)]*\.(?:woff|ttf)\)format\("(?:woff|truetype)"\)/g, "");
        if (cut === css) continue;
        asset.source = cut;
        stripped = true;
      }
      if (!stripped) {
        this.error("no stylesheet had the layered font sources this build expects");
      }

      const referenced = Object.values(bundle)
        .map((output) => (output.type === "asset" ? String(output.source) : output.code))
        .join("");
      for (const [name, output] of Object.entries(bundle)) {
        if (!/\.(?:woff|ttf)$/.test(output.fileName)) continue;
        if (referenced.includes(output.fileName)) continue;
        delete bundle[name];
      }
    },
  };
}

// The Go binary embeds the built assets, so the output lands outside this
// folder and keeps a flat assets directory. In development the dev server
// forwards /api to the harness instead, since the two run on different ports.
export default defineConfig({
  plugins: [oneIconWeight(), woff2Only(), svelte(), tailwindcss()],
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
    // The interface is one chunk, served from the binary over the loopback
    // interface, so the default 500 kB is not the line worth watching. This
    // sits just above what the build produces, to report growth rather than
    // the size that is already understood.
    chunkSizeWarningLimit: 600,
  },
});
