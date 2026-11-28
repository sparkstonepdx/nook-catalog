package com.sparkstonepdx.push;

import java.math.BigInteger;
import java.nio.ByteBuffer;
import java.security.AlgorithmParameters;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.interfaces.ECPublicKey;
import java.security.spec.ECGenParameterSpec;
import java.security.spec.ECParameterSpec;
import java.security.spec.ECPoint;
import java.security.spec.ECPublicKeySpec;
import java.util.Arrays;
import javax.crypto.Cipher;
import javax.crypto.KeyAgreement;
import javax.crypto.Mac;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

/**
 * The receiving side of Web Push encryption (RFC 8291, aes128gcm from RFC 8188): what the push app does for a browser,
 * it does for this device when it subscribes with {@link #publicKey} and an auth secret. Plain Java, no Android APIs,
 * so it runs in unit tests against the push server's output.
 */
public final class PushCrypto {
    private PushCrypto() {}

    public static KeyPair newKeyPair() throws GeneralSecurityException {
        KeyPairGenerator g = KeyPairGenerator.getInstance("EC");
        g.initialize(new ECGenParameterSpec("secp256r1"));
        return g.generateKeyPair();
    }

    /** The 65-byte uncompressed point a subscription sends as keys.p256dh. */
    public static byte[] publicKey(ECPublicKey k) {
        byte[] out = new byte[65];
        out[0] = 4;
        copy32(k.getW().getAffineX(), out, 1);
        copy32(k.getW().getAffineY(), out, 33);
        return out;
    }

    private static void copy32(BigInteger n, byte[] out, int at) {
        byte[] b = n.toByteArray(); // big-endian, maybe with a sign byte or fewer than 32 bytes
        int len = Math.min(b.length, 32);
        System.arraycopy(b, b.length - len, out, at + 32 - len, len);
    }

    private static ECPublicKey point(byte[] raw) throws GeneralSecurityException {
        if (raw.length != 65 || raw[0] != 4) throw new GeneralSecurityException("not an uncompressed P-256 point");
        AlgorithmParameters p = AlgorithmParameters.getInstance("EC");
        p.init(new ECGenParameterSpec("secp256r1"));
        ECParameterSpec spec = p.getParameterSpec(ECParameterSpec.class);
        ECPoint w = new ECPoint(new BigInteger(1, Arrays.copyOfRange(raw, 1, 33)), new BigInteger(1, Arrays.copyOfRange(raw, 33, 65)));
        return (ECPublicKey) KeyFactory.getInstance("EC").generatePublic(new ECPublicKeySpec(w, spec));
    }

    private static byte[] hmac(byte[] key, byte[]... parts) throws GeneralSecurityException {
        Mac m = Mac.getInstance("HmacSHA256");
        m.init(new SecretKeySpec(key, "HmacSHA256"));
        for (byte[] p : parts) m.update(p);
        return m.doFinal();
    }

    /** HKDF-SHA256 with an output of at most 32 bytes: one expand round. */
    private static byte[] hkdf(byte[] salt, byte[] ikm, byte[] info, int len) throws GeneralSecurityException {
        byte[] prk = hmac(salt, ikm);
        return Arrays.copyOf(hmac(prk, info, new byte[] {1}), len);
    }

    private static byte[] concat(byte[]... parts) {
        int n = 0;
        for (byte[] p : parts) n += p.length;
        ByteBuffer b = ByteBuffer.allocate(n);
        for (byte[] p : parts) b.put(p);
        return b.array();
    }

    /** Decrypts one aes128gcm push body for the device with this key pair and auth secret. */
    public static byte[] decrypt(byte[] body, PrivateKey priv, byte[] uaPublic, byte[] auth) throws GeneralSecurityException {
        if (body.length < 21 + 65 + 17) throw new GeneralSecurityException("too short");
        byte[] salt = Arrays.copyOfRange(body, 0, 16);
        int idlen = body[20] & 0xff;
        if (idlen != 65) throw new GeneralSecurityException("unexpected key id length " + idlen);
        byte[] asPublic = Arrays.copyOfRange(body, 21, 21 + idlen);
        byte[] ct = Arrays.copyOfRange(body, 21 + idlen, body.length);

        KeyAgreement ka = KeyAgreement.getInstance("ECDH");
        ka.init(priv);
        ka.doPhase(point(asPublic), true);
        byte[] shared = ka.generateSecret();

        byte[] ikm = hkdf(auth, shared, concat("WebPush: info\0".getBytes(), uaPublic, asPublic), 32);
        byte[] cek = hkdf(salt, ikm, "Content-Encoding: aes128gcm\0".getBytes(), 16);
        byte[] nonce = hkdf(salt, ikm, "Content-Encoding: nonce\0".getBytes(), 12);

        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.DECRYPT_MODE, new SecretKeySpec(cek, "AES"), new GCMParameterSpec(128, nonce));
        byte[] plain = c.doFinal(ct);
        int end = plain.length - 1; // padding is zeros before the delimiter; the last record's delimiter is 2
        while (end >= 0 && plain[end] == 0) end--;
        if (end < 0 || plain[end] != 2) throw new GeneralSecurityException("bad record delimiter");
        return Arrays.copyOf(plain, end);
    }
}
