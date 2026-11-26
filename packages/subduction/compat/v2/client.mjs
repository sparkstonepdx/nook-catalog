// The 2.6 client, for compat.sh:  make WS  |  read WS DOC  |  set WS DOC KEY VALUE
// Prints a doc's URL (make) or its contents as JSON (read, set). Exits non-zero if the server doesn't answer.
import { Repo, WebSocketEndpoint, initSubduction } from "@automerge/automerge-repo";
await initSubduction();
const [cmd, ws, doc, key, value] = process.argv.slice(2);
const repo = new Repo({ network: [], subductionWebsocketEndpoints: [new WebSocketEndpoint(ws)] });
const heard = new Set();
repo.on("subduction-remote-heads", ({ documentId }) => heard.add(documentId));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const until = async (f, ms = 20000) => { for (const t = Date.now(); Date.now() - t < ms; await sleep(200)) if (f()) return true; return false; };
const find = (id) => Promise.race([repo.find(id), sleep(20000).then(() => { throw new Error("find timed out: " + id); })]);
let h;
if (cmd === "make") {
  h = repo.create({ by: ["v2"] });
  if (!(await until(() => heard.has(h.documentId)))) throw new Error("the server never confirmed the new doc");
  console.log(h.url);
} else {
  h = await find(doc);
  await sleep(1500);
  if (cmd === "set") {
    h.change((d) => { d[key] = value; });
    heard.delete(h.documentId);
    if (!(await until(() => heard.has(h.documentId)))) throw new Error("the server never confirmed the edit");
  }
  console.log(JSON.stringify(h.doc()));
}
process.exit(0);
