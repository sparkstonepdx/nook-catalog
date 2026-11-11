import { For, Show, createSignal, onCleanup, onSettled } from "solid-js";
import { apps, appPath, generated, installLink, official, repo, type App as CatalogApp } from "./catalog";

const NOOK = "https://github.com/sparkstonepdx/nook";
const KEY = "nook-dashboard";

// One component for every page, picked by path: the prerender renders each path, and the client hydrates
// the same one.
export default function App(props: { path: string }) {
  const app = () => apps.find((a) => props.path === appPath(a.id));
  return (
    <>
      <header class="navbar navbar-flush top-bar">
        <a href="/" class="brand">nook <span class="brand-sub">apps</span></a>
        <a class="btn btn-ghost btn-sm" href={NOOK}>Get nook</a>
      </header>
      <main class="container page">
        <Show when={props.path === "/"} fallback={<Show when={app()} fallback={<NotFound />}>{(a) => <AppPage app={a()} />}</Show>}>
          <Home />
        </Show>
      </main>
      <footer class="site-footer">
        <span>{official ? "The official nook catalog." : `A community catalog, ${repo}.`} Every download is pinned by its hash and the list is signed.</span>
        <a class="link" href={`https://github.com/${repo}`}>Source</a>
      </footer>
    </>
  );
}

function Home() {
  return (
    <>
      <section class="hero">
        <p class="eyebrow">{official ? "Official catalog" : "Community catalog"}</p>
        <h1>Apps for your nook</h1>
        <p class="lede">Each one installs on your own server as its own app, with its own user, folder and limits. Updates follow the policy you pick and roll back by themselves if they don't come up.</p>
      </section>
      <ul class="list">
        <For each={apps}>
          {(a) => (
            <li><a class="row" href={appPath(a.id)}>
              <span class="app-coin" aria-hidden="true">{a.name.slice(0, 1)}</span>
              <span class="row-main"><span>{a.name} <Verified /></span><small>{a.description}</small></span>
              <span class="row-end"><span class="mono">{a.version}</span><span class="chev">›</span></span>
            </a></li>
          )}
        </For>
      </ul>
      <p class="hint">{apps.length} {apps.length === 1 ? "app" : "apps"}, checked {new Date(generated).toISOString().slice(0, 10)}.</p>
    </>
  );
}

function Verified() {
  return <Show when={official}><span class="badge badge-success verified" title="In the official nook catalog"><span aria-hidden="true">✓</span> Verified</span></Show>;
}

function AppPage(props: { app: CatalogApp }) {
  const [open, setOpen] = createSignal(false);
  const a = () => props.app;
  const arches = () => Object.keys(a().artifacts ?? {}).join(", ");
  return (
    <>
      <div class="back-row"><a class="btn btn-ghost btn-sm back" href="/">‹ All apps</a></div>
      <section class="app-head">
        <span class="app-coin big" aria-hidden="true">{a().name.slice(0, 1)}</span>
        <div class="app-title">
          <h1>{a().name}</h1>
          <p class="cmd-line"><Verified /> <span class="mono text-secondary">{a().version}</span></p>
        </div>
      </section>
      <p class="lede">{a().description}</p>
      <div class="install-row">
        <button class="btn btn-lg" onClick={() => setOpen(true)}>Install on nook</button>
        <Show when={a().homepage}><a class="btn btn-secondary btn-lg" href={a().homepage}>Homepage</a></Show>
      </div>

      <section class="section">
        <h2>Details</h2>
        <dl class="facts card">
          <dt>Kind</dt><dd>{{ web: "A website at its own address", service: "A service other apps use", cron: "Runs on a schedule" }[a().kind || "web"]}</dd>
          <Show when={a().license}><dt>License</dt><dd>{a().license}</dd></Show>
          <Show when={a().memory}><dt>Memory</dt><dd>up to {a().memory}</dd></Show>
          <Show when={arches()}><dt>Runs on</dt><dd>{arches()}</dd></Show>
          <Show when={a().needs?.length}><dt>Needs</dt><dd>{a().needs!.join(", ")}</dd></Show>
        </dl>
      </section>

      <Show when={a().env?.length}>
        <section class="section">
          <h2>It asks for</h2>
          <ul class="list">
            <For each={a().env}>
              {(f) => <li class="row"><span class="row-main"><span class="mono">{f.key}</span><small>{f.desc}<Show when={f.default}> · default <span class="mono">{f.default}</span></Show></small></span></li>}
            </For>
          </ul>
        </section>
      </Show>

      <Show when={open()}><InstallSheet app={a()} onClose={() => setOpen(false)} /></Show>
    </>
  );
}

// Asks once for your dashboard's address, then opens the install sheet there. The address stays in this
// browser only.
function InstallSheet(props: { app: CatalogApp; onClose: () => void }) {
  const [host, setHost] = createSignal("");
  const [copied, setCopied] = createSignal(false);
  const opened = performance.now();
  let input!: HTMLInputElement;
  onSettled(() => {
    try { setHost(localStorage.getItem(KEY) || ""); } catch { /* storage off */ }
    const key = (e: KeyboardEvent) => e.key === "Escape" && props.onClose();
    document.addEventListener("keydown", key);
    document.body.style.overflow = "hidden";
    input?.focus();
    onCleanup(() => { document.removeEventListener("keydown", key); document.body.style.overflow = ""; });
  });
  const cli = () => official
    ? `nook-admin store install ${props.app.id}`
    : `nook-admin store catalogs add ${repo}\nnook-admin store install ${props.app.id} --catalog ${repo.toLowerCase().replace("/", "-")}`;
  const go = (e: SubmitEvent) => {
    e.preventDefault();
    const h = host().trim();
    if (!h) return;
    try { localStorage.setItem(KEY, h); } catch { /* storage off */ }
    location.href = installLink(h, props.app.id);
  };
  return (
    <div class="sheet-backdrop" onClick={(e) => e.target === e.currentTarget && performance.now() - opened > 400 && props.onClose()}>
      <div class="sheet" role="dialog" aria-modal="true" aria-label={`Install ${props.app.name}`}>
        <div class="sheet-grip" />
        <div class="sheet-header">
          <h2>Install {props.app.name}</h2>
          <button class="icon-btn" aria-label="Close" onClick={() => props.onClose()}>×</button>
        </div>
        <div class="sheet-body">
          <form id="install" class="stack" onSubmit={go}>
            <label class="label">Your nook dashboard
              <input ref={input} class="input mono" name="host" value={host()} onInput={(e) => setHost(e.currentTarget.value)} placeholder="admin.example.com" inputmode="url" autocomplete="url" autocapitalize="off" spellcheck={false} required />
              <small class="hint">Opens the install there, where you pick its name and address. Only this browser remembers it.</small>
            </label>
            <Show when={!official}><div class="alert alert-warning"><span>Your nook adds this community catalog first and asks you to trust it.</span></div></Show>
          </form>
          <details class="cli">
            <summary>Or on the server</summary>
            <pre class="output">{cli()}</pre>
            <button class="btn btn-secondary btn-sm" onClick={() => { navigator.clipboard?.writeText(cli()); setCopied(true); }}>{copied() ? "Copied" : "Copy"}</button>
          </details>
          <p class="hint">No nook yet? <a class="link" href={NOOK}>Set one up</a> on any Linux server.</p>
        </div>
        <div class="sheet-footer">
          <button class="btn btn-secondary" onClick={() => props.onClose()}>Cancel</button>
          <button class="btn" type="submit" form="install">Open in nook</button>
        </div>
      </div>
    </div>
  );
}

function NotFound() {
  return <><h1>Not here</h1><p class="lede">That app isn't in this catalog. <a class="link" href="/">See all apps</a></p></>;
}
