// The 3.0 client, for compat.sh:  make WS  |  read WS DOC  |  set WS DOC KEY VALUE
// Prints a doc's URL (make) or its contents as JSON (read, set). Exits non-zero if the server doesn't answer.
import { Repo } from "@automerge/automerge-repo";
import { SubductionBackend } from "@automerge/automerge-repo-subduction";
import { DummySubductionStorageAdapter } from "@automerge/automerge-repo-subduction/helpers/DummySubductionStorageAdapter.js";
import { WebSocketSubductionNetworkAdapter } from "@automerge/automerge-repo-subduction-network-websocket";
import * as sub from "@automerge/subduction";
if (sub.default) await sub.default();
const [cmd, ws, doc, key, value] = process.argv.slice(2);
const repo = new Repo({ backend: new SubductionBackend({ storage: new DummySubductionStorageAdapter(), network: [new WebSocketSubductionNetworkAdapter(ws)] }) });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const find = (id) => Promise.race([repo.find(id), sleep(20000).then(() => { throw new Error("find timed out: " + id); })]);
let h;
if (cmd === "make") {
  h = await repo.create({ by: ["v3"] });
  await repo.flush();
  await sleep(4000); // no confirmation event in 3.0: compat.sh reads it back through fresh clients
  console.log(h.url);
} else {
  h = await find(doc);
  await sleep(1500);
  if (cmd === "set") {
    h.change((d) => { d[key] = value; });
    await repo.flush();
    await sleep(4000);
  }
  console.log(JSON.stringify(h.doc()));
}
process.exit(0);
