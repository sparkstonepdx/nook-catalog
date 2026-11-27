import Capacitor
import Foundation
import UserNotifications

/// NookPush on iOS: the APNs token and this device's push keys, for subscribing with the nook push app, and the
/// pushes it gets as pushReceived (app open) and pushOpened (notification tapped). Encrypted pushes are decrypted by
/// the app's Notification Service Extension before they're shown (NotificationServiceExtension/ in this package).
@objc(NookPushPlugin)
public class NookPushPlugin: CAPPlugin, CAPBridgedPlugin, NotificationHandlerProtocol {
    public let identifier = "NookPushPlugin"
    public let jsName = "NookPush"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "register", returnType: CAPPluginReturnPromise)
    ]
    private var pending: CAPPluginCall?

    override public func load() {
        bridge?.notificationRouter.pushNotificationHandler = self
        NotificationCenter.default.addObserver(self, selector: #selector(didRegister(_:)), name: .capacitorDidRegisterForRemoteNotifications, object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(didFail(_:)), name: .capacitorDidFailToRegisterForRemoteNotifications, object: nil)
    }

    @objc func register(_ call: CAPPluginCall) {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, error in
            if let error = error { call.reject(error.localizedDescription); return }
            if !granted { call.reject("notifications aren't allowed"); return }
            self.pending = call
            DispatchQueue.main.async { UIApplication.shared.registerForRemoteNotifications() }
        }
    }

    @objc func didRegister(_ n: Notification) {
        guard let call = pending, let token = n.object as? Data else { return }
        pending = nil
        do {
            guard let keys = try NookPushKeys.load() else { call.reject("couldn't make this device's push keys"); return }
            #if DEBUG
            let sandbox = true // run from Xcode: a development token, which only Apple's sandbox takes
            #else
            let sandbox = false
            #endif
            call.resolve([
                "kind": "apns",
                "token": token.map { String(format: "%02x", $0) }.joined(),
                "sandbox": sandbox,
                "keys": ["p256dh": keys.publicKey.base64URL, "auth": keys.auth.base64URL]
            ])
        } catch {
            call.reject("couldn't make this device's push keys: \(error)")
        }
    }

    @objc func didFail(_ n: Notification) {
        pending?.reject((n.object as? Error)?.localizedDescription ?? "couldn't register for pushes")
        pending = nil
    }

    /// The payload the sender sent: decrypted by the extension (a JSON string), or as the push app sent it.
    private func payload(_ info: [AnyHashable: Any]) -> Any? {
        if let s = info["payload"] as? String, let d = s.data(using: .utf8), let obj = try? JSONSerialization.jsonObject(with: d) {
            return JSTypes.coerceDictionaryToJSObject(["p": obj])?["p"]
        }
        if let d = info["payload"] as? [String: Any] { return JSTypes.coerceDictionaryToJSObject(d) }
        return nil
    }

    public func willPresent(notification: UNNotification) -> UNNotificationPresentationOptions {
        guard let p = payload(notification.request.content.userInfo) else { return [.banner, .list, .sound] }
        notifyListeners("pushReceived", data: ["payload": p])
        return [] // the open app has it, as on Android
    }

    public func didReceive(response: UNNotificationResponse) {
        guard let p = payload(response.notification.request.content.userInfo) else { return }
        notifyListeners("pushOpened", data: ["payload": p], retainUntilConsumed: true)
    }
}
