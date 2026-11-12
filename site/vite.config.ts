import { defineConfig, type Plugin } from "vite";
import solid from "vite-plugin-solid";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";

// virtual:catalog-index is the signed index.json the workflow built (INDEX=../index.json). Without one,
// for `npm run dev`, it's read from apps/*.json with the versions left unpinned.
function catalogIndex(): Plugin {
  const id = "virtual:catalog-index";
  return {
    name: "catalog-index",
    resolveId: (s) => (s === id ? "\0" + id : undefined),
    load(s) {
      if (s !== "\0" + id) return;
      if (process.env.INDEX) return `export default ${readFileSync(resolve(process.env.INDEX), "utf8")}`;
      const dir = resolve(__dirname, "../apps");
      const apps = readdirSync(dir).filter((f) => f.endsWith(".json")).map((f) => {
        const a = JSON.parse(readFileSync(resolve(dir, f), "utf8"));
        const { source, ...rest } = a;
        return { ...rest, repo: rest.repo || (source?.github ? `https://github.com/${source.github}` : undefined), version: source?.tag || "latest", artifacts: Object.fromEntries(Object.keys(source?.assets ?? {}).map((k) => [k, { url: "" }])) };
      });
      return `export default ${JSON.stringify({ generated: new Date().toISOString(), apps })}`;
    },
  };
}

// `npm run dev` serves the repo's images/ at /images, as the build does.
function repoImages(): Plugin {
  return {
    name: "repo-images",
    configureServer(server) {
      server.middlewares.use("/images", (req, res, next) => {
        const file = resolve(__dirname, "../images", "." + decodeURIComponent((req.url || "").split("?")[0]));
        if (!file.startsWith(resolve(__dirname, "../images")) || !existsSync(file)) return next();
        res.setHeader("Content-Type", file.endsWith(".png") ? "image/png" : "image/jpeg");
        res.end(readFileSync(file));
      });
    },
  };
}

// Built twice: the client bundle that hydrates each page, and the server bundle prerender.mjs uses to
// write every page's HTML.
export default defineConfig({ plugins: [catalogIndex(), repoImages(), solid({ ssr: true })], build: { emptyOutDir: true } });
