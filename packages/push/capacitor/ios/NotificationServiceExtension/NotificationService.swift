// The Notification Service Extension for encrypted pushes from the nook push app. Add an extension target to the app
// (File > New > Target > Notification Service Extension), replace its NotificationService.swift with this file, and add
// node_modules/@sparkstone/capacitor-push/ios/Sources/NookPushPlugin/NookPushCrypto.swift to the extension target too.
// Give the app and the extension the same keychain group (Signing & Capabilities > Keychain Sharing) and put its full
// name in both Info.plists as NookPushKeychainGroup.
//
// Apple delivers "New notification" with the payload encrypted in "enc"; this decrypts it on the phone and shows the
// real title and body. If it can't, the placeholder is shown as is.
import UserNotifications

class NotificationService: UNNotificationServiceExtension {
    override func didReceive(_ request: UNNotificationRequest, withContentHandler done: @escaping (UNNotificationContent) -> Void) {
        let content = (request.content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
        defer { done(content) }
        guard let enc = request.content.userInfo["enc"] as? String,
              let keys = try? NookPushKeys.load(create: false),
              let json = try? keys.open(enc),
              let obj = try? JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any] else { return }
        if let t = obj["title"] as? String { content.title = t }
        if let b = obj["body"] as? String { content.body = b }
        var info = content.userInfo
        info.removeValue(forKey: "enc")
        info["payload"] = json // the plugin reads it as pushReceived / pushOpened
        content.userInfo = info
    }
}
