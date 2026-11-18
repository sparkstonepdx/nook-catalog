// The signed index the workflow just built (or a sample for `npm run dev`), and where it's published.
import index from "virtual:catalog-index";

export type EnvField = { key: string; desc?: string; default?: string; secret?: boolean; required?: boolean };
export type Image = { path: string; sha256?: string; alt?: string; width?: number; height?: number };
export type App = {
  id: string; name: string; description: string; homepage?: string; repo?: string; license?: string; version: string;
  icon?: Image; screenshots?: Image[];
  kind?: string; versions?: string[]; needs?: string[]; provides?: Record<string, { desc?: string }>; ports?: number[]; env?: EnvField[]; memory?: string; artifacts: Record<string, { url: string }>;
};

export const apps: App[] = [...(index.apps as App[])].sort((a, b) => a.name.localeCompare(b.name));
export const generated: string = index.generated;
// Which catalog this is. The official one installs straight away; any other repo built from this
// template is added on the server first, under its own key.
export const repo: string = import.meta.env.VITE_CATALOG_REPO || "sparkstonepdx/nook-catalog";
export const official = repo === "sparkstonepdx/nook-catalog";
export const appPath = (id: string) => `/apps/${id}/`;
// Images are copied into the site at build (prerender.mjs), at the same paths as in the repo.
export const imageURL = (i: Image) => `/${i.path}`;

// The link that opens the install sheet on someone's own nook dashboard.
export function installLink(dashboard: string, id: string) {
  const host = dashboard.trim().replace(/^https?:\/\//, "").replace(/\/.*$/, "");
  const q = new URLSearchParams({ app: id });
  if (official) q.set("catalog", "official");
  else q.set("source", repo);
  return `https://${host}/store/install?${q}`;
}

// What an app provides, its description with {version} filled in, and which apps provide a need ("email?" is optional).
export const provided = (a: App) => Object.entries(a.provides ?? {}).map(([name, p]) => ({ name, desc: p.desc?.replaceAll("{version}", orList(a.versions) || a.version) }));
export const providers = (need: string) => apps.filter((a) => a.provides?.[need.replace(/[?<>=~^].*$/, "")]);
const orList = (v?: string[]) => (v?.length ? (v.length > 1 ? `${v.slice(0, -1).join(", ")} or ${v.at(-1)}` : v[0]) : "");
export const users = (service: string) => apps.filter((a) => a.needs?.some((n) => n.replace(/[?<>=~^].*$/, "") === service));
