import { registerPlugin, type PluginListenerHandle } from "@capacitor/core";

/** What a device subscribes to the nook push app with: its token, and the keys its pushes are encrypted to. */
export type Registration = {
  kind: "apns" | "fcm";
  token: string;
  /** iOS builds run from Xcode get development tokens, which only Apple's sandbox takes. */
  sandbox?: boolean;
  keys: { p256dh: string; auth: string };
};

/** A push as its sender sent it (`payload` in POST /v1/send/HANDLE), decrypted on the device. */
export type Push = { payload: any };

export interface NookPushPlugin {
  /** Asks for permission, then gets this device's token and push keys. */
  register(): Promise<Registration>;
  /** A push arrived while the app was open (nothing is shown; the app decides). */
  addListener(event: "pushReceived", fn: (p: Push) => void): Promise<PluginListenerHandle>;
  /** The notification for a push was tapped. Kept until a listener takes it, so a cold start gets it too. */
  addListener(event: "pushOpened", fn: (p: Push) => void): Promise<PluginListenerHandle>;
  /** Android only: FCM changed the token; subscribe again with it. */
  addListener(event: "tokenChanged", fn: (e: { token: string }) => void): Promise<PluginListenerHandle>;
}

export const NookPush = registerPlugin<NookPushPlugin>("NookPush");

/** The body for the push app's POST /v1/subscribe/APP. */
export function subscription(r: Registration) {
  return r.kind === "apns" ? { apns: { token: r.token, sandbox: !!r.sandbox }, keys: r.keys } : { fcm: { token: r.token }, keys: r.keys };
}

/**
 * Registers this device and subscribes it with the push app (`cfg` is the app's push.json). Returns the handle to share
 * with whoever may notify this device, and the secret that unsubscribes it; keep the secret on the device.
 */
export async function subscribe(cfg: { subscribe: string }): Promise<{ handle: string; secret: string }> {
  const r = await NookPush.register();
  const res = await fetch(cfg.subscribe, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(subscription(r)) });
  if (!res.ok) throw new Error(`push subscribe: ${res.status} ${(await res.json().catch(() => ({}))).error ?? ""}`);
  return res.json();
}
