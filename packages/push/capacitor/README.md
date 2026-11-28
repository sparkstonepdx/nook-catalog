# @sparkstone/capacitor-push

Native push for Capacitor apps through the [nook push app](../README.md): APNs on iOS, FCM on Android, with every push
encrypted to the device. Apple and Google carry only ciphertext; the device decrypts it and shows the notification.
Use it instead of `@capacitor/push-notifications` (both can't own FCM's messaging service).

```js
import { NookPush, subscribe } from "@sparkstone/capacitor-push";

const cfg = await (await fetch("/push.json")).json(); // from the app's build, see the push README
const { handle, secret } = await subscribe(cfg);        // asks permission, registers, subscribes with keys
// share `handle` with whoever may notify this device; keep `secret` here

NookPush.addListener("pushReceived", ({ payload }) => { /* the app is open: show it your way */ });
NookPush.addListener("pushOpened", ({ payload }) => { /* a notification was tapped: go to payload.url */ });
NookPush.addListener("tokenChanged", () => subscribe(cfg)); // Android: FCM rotated the token
```

Senders don't change: `POST /v1/send/HANDLE` with `{"payload": {"title", "body", ...}}`. A `title` or `body` is shown
as the notification; the whole payload reaches the app.

## Android

1. Add the app to a Firebase project and put its `google-services.json` in `android/app/`, with the
   `com.google.gms.google-services` Gradle plugin applied (as for any FCM app).
2. Give the push app the project's service account (push README, `PUT /v1/native/fcm`).
3. Optional: a monochrome `res/drawable/ic_stat_notify` for the status bar icon (otherwise the app icon).

Pushes arrive as data-only messages, decrypted by the plugin's `PushService` and shown on the `nook-push` channel.
Tapping one opens the app with `pushOpened`. Keys are in the app's private preferences.

## iOS

1. Enable Push Notifications under Signing & Capabilities, and give the push app the APNs key
   (`PUT /v1/native/apns`).
2. Add a Notification Service Extension target (File > New > Target), replace its `NotificationService.swift` with
   [`ios/NotificationServiceExtension/NotificationService.swift`](ios/NotificationServiceExtension/NotificationService.swift),
   and add `node_modules/@sparkstone/capacitor-push/ios/Sources/NookPushPlugin/NookPushCrypto.swift` to the extension
   target as well.
3. Turn on Keychain Sharing for both the app and the extension with the same group, and put the group's full name
   (team prefix included, like `ABCDE12345.com.example.app.push`) in both Info.plists as `NookPushKeychainGroup`.
   That's how the extension reads the device's key.

Apple delivers a placeholder ("New notification") with the ciphertext; the extension decrypts it and swaps in the
real title and body before it's shown. If it can't, the placeholder is what shows. Builds run from Xcode register as
`sandbox` and go through Apple's sandbox.

## Tests

`npm test` decrypts pushes the server encrypted (`testdata/vectors.json`, written by the server's `TestWriteVectors`)
with the Android code, and checks a tampered one fails. The packages workflow runs it. The iOS code uses the same steps
through CryptoKit and isn't run in CI.
