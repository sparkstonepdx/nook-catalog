// This device's push keys and the decryption of pushes the nook push app encrypted to them (Web Push encryption,
// RFC 8291 with aes128gcm from RFC 8188). Used by the plugin and by the app's Notification Service Extension, which
// compiles this same file: it depends on nothing but Foundation, CryptoKit and Security.
//
// The keys are a P-256 key pair and a 16-byte auth secret, made once and kept in the keychain. For the extension to
// read them, the app and the extension share a keychain group, named in both Info.plists as NookPushKeychainGroup
// (with the team prefix, like ABCDE12345.com.example.app.push).
import CryptoKit
import Foundation
import Security

public enum NookPushError: Error {
    case keychain(OSStatus)
    case malformed(String)
}

public struct NookPushKeys {
    public let privateKey: P256.KeyAgreement.PrivateKey
    public let auth: Data

    /// The 65-byte uncompressed point a subscription sends as keys.p256dh.
    public var publicKey: Data { privateKey.publicKey.x963Representation }

    static let service = "com.sparkstonepdx.push"

    static func query() -> [String: Any] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: "keys"]
        if let group = Bundle.main.object(forInfoDictionaryKey: "NookPushKeychainGroup") as? String, !group.isEmpty {
            q[kSecAttrAccessGroup as String] = group
        }
        return q
    }

    /// The keys, made and saved the first time. The extension only reads (create: false).
    public static func load(create: Bool = true) throws -> NookPushKeys? {
        var q = query()
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecSuccess, let d = out as? Data, d.count == 48 {
            return NookPushKeys(privateKey: try P256.KeyAgreement.PrivateKey(rawRepresentation: d.prefix(32)), auth: d.suffix(16))
        }
        if status != errSecItemNotFound && status != errSecSuccess { throw NookPushError.keychain(status) }
        if !create { return nil }
        let k = P256.KeyAgreement.PrivateKey()
        var auth = Data(count: 16)
        let ok = auth.withUnsafeMutableBytes { SecRandomCopyBytes(kSecRandomDefault, 16, $0.baseAddress!) }
        if ok != errSecSuccess { throw NookPushError.keychain(ok) }
        var add = query()
        add[kSecValueData as String] = k.rawRepresentation + auth
        // the extension runs while the phone is locked, after the first unlock since it started
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let added = SecItemAdd(add as CFDictionary, nil)
        if added != errSecSuccess { throw NookPushError.keychain(added) }
        return NookPushKeys(privateKey: k, auth: auth)
    }

    /// Decrypts one aes128gcm push body ("enc", base64url) into the JSON text the sender sent.
    public func open(_ enc: String) throws -> String {
        guard let body = Data(base64URL: enc), body.count >= 21 + 65 + 17 else { throw NookPushError.malformed("too short") }
        let salt = body.subdata(in: 0..<16)
        let idlen = Int(body[body.startIndex + 20])
        guard idlen == 65 else { throw NookPushError.malformed("key id length \(idlen)") }
        let asPublic = body.subdata(in: 21..<(21 + idlen))
        let ct = body.subdata(in: (21 + idlen)..<body.count)

        let shared = try privateKey.sharedSecretFromKeyAgreement(with: P256.KeyAgreement.PublicKey(x963Representation: asPublic))
        let ecdh = shared.withUnsafeBytes { Data($0) }
        let ikm = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: ecdh), salt: auth,
                                         info: Data("WebPush: info\0".utf8) + publicKey + asPublic, outputByteCount: 32)
        let ikmData = ikm.withUnsafeBytes { Data($0) }
        let cek = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: ikmData), salt: salt,
                                         info: Data("Content-Encoding: aes128gcm\0".utf8), outputByteCount: 16)
        let nonce = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: ikmData), salt: salt,
                                           info: Data("Content-Encoding: nonce\0".utf8), outputByteCount: 12)
        let box = try AES.GCM.SealedBox(nonce: AES.GCM.Nonce(data: nonce.withUnsafeBytes { Data($0) }),
                                        ciphertext: ct.prefix(ct.count - 16), tag: ct.suffix(16))
        var plain = try AES.GCM.open(box, using: cek)
        while let last = plain.last, last == 0 { plain.removeLast() } // padding before the delimiter
        guard plain.last == 2 else { throw NookPushError.malformed("record delimiter") }
        plain.removeLast()
        guard let text = String(data: plain, encoding: .utf8) else { throw NookPushError.malformed("not UTF-8") }
        return text
    }
}

extension Data {
    init?(base64URL s: String) {
        var b = s.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while b.count % 4 != 0 { b += "=" }
        self.init(base64Encoded: b)
    }

    var base64URL: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }
}
