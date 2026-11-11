import { renderToString, generateHydrationScript } from "@solidjs/web";
import App from "./App";
import { apps, appPath } from "./catalog";

export const paths = ["/", ...apps.map((a) => appPath(a.id)), "/404.html"];
export const pages = Object.fromEntries(apps.map((a) => [appPath(a.id), { title: `${a.name}: nook apps`, desc: a.description }]));

export function render(path: string) {
  return { html: renderToString(() => <App path={path} />), head: generateHydrationScript() };
}
