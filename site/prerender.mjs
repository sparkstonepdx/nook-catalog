// Writes every page's HTML: renders each path with the server bundle into the client build's index.html.
import { readFileSync, writeFileSync, mkdirSync, rmSync, cpSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { render, paths, pages } from "./dist/server/entry-server.js";

const out = "dist/client";
const template = readFileSync(join(out, "index.html"), "utf8");
const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
const site = { title: "nook apps", desc: "Apps for your nook server: checked, signed, and installed as their own app." };

for (const path of paths) {
  const { html, head } = render(path === "/404.html" ? "/404" : path);
  const meta = pages[path] ?? site;
  const page = template
    .replace("<!--head-->", `<title>${esc(meta.title)}</title>\n    <meta name="description" content="${esc(meta.desc)}" />\n    <meta property="og:title" content="${esc(meta.title)}" />\n    <meta property="og:description" content="${esc(meta.desc)}" />\n    ${head}`)
    .replace("<!--app-->", html);
  const file = path.endsWith(".html") ? join(out, path) : join(out, path, "index.html");
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, page);
  console.log("wrote", file);
}
rmSync("dist/server", { recursive: true, force: true });
// icons and screenshots, at the same paths as in the repo
if (existsSync("../images")) cpSync("../images", join(out, "images"), { recursive: true });
