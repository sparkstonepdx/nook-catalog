// Checks push's output with the libraries browsers' push stacks are tested against:
//   cd scripts && npm i http_ece@1 web-push@3 && node interop.mjs
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const ece = require("http_ece");
const webpush = require("web-push");

const ua = crypto.createECDH("prime256v1");
ua.generateKeys();
const auth = crypto.randomBytes(16);
const vapid = webpush.generateVAPIDKeys(); // keys made by web-push must import as they are
const out = execFileSync("go", ["test", "-run", "TestInterop", "-count=1", "-v", "."], {
  cwd: new URL("..", import.meta.url).pathname,
  env: { ...process.env, INTEROP_P256DH: ua.getPublicKey("base64url"), INTEROP_AUTH: auth.toString("base64url"), INTEROP_VAPID_PRIVATE: vapid.privateKey, INTEROP_VAPID_PUBLIC: vapid.publicKey },
}).toString();
const body = Buffer.from(out.match(/^BODY=(.*)$/m)[1], "base64url");
const header = out.match(/^AUTH=(.*)$/m)[1];

const plain = ece.decrypt(body, { version: "aes128gcm", privateKey: ua, authSecret: auth });
if (plain.toString() !== '{"title":"hello from push"}') throw new Error("decrypted to " + plain);
console.log("http_ece decrypts it:", plain.toString());

const [, jwt, k] = header.match(/^vapid t=([^,]+), k=(.+)$/);
if (k !== vapid.publicKey) throw new Error("k isn't the imported web-push public key");
const [h, c, sig] = jwt.split(".");
const pub = Buffer.from(k, "base64url");
const key = crypto.createPublicKey({ key: { kty: "EC", crv: "P-256", x: pub.subarray(1, 33).toString("base64url"), y: pub.subarray(33).toString("base64url") }, format: "jwk" });
const ok = crypto.verify("sha256", Buffer.from(h + "." + c), { key, dsaEncoding: "ieee-p1363" }, Buffer.from(sig, "base64url"));
if (!ok) throw new Error("VAPID signature doesn't verify");
console.log("VAPID JWT verifies with the web-push key:", JSON.parse(Buffer.from(c, "base64url")));
