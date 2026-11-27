package com.sparkstonepdx.push;

import java.math.BigInteger;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.AlgorithmParameters;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.PrivateKey;
import java.security.interfaces.ECPublicKey;
import java.security.spec.ECGenParameterSpec;
import java.security.spec.ECParameterSpec;
import java.security.spec.ECPrivateKeySpec;
import java.util.Base64;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Decrypts what the push server encrypted (testdata/vectors.json, written by the server's TestWriteVectors).
 * Plain Java, no test framework: javac and java run it (capacitor/test.sh).
 */
public class PushCryptoTest {
    static String field(String json, String key) {
        Matcher m = Pattern.compile("\"" + key + "\":\\s*\"([^\"]*)\"").matcher(json);
        if (!m.find()) throw new AssertionError("no " + key);
        return m.group(1);
    }

    static byte[] b64(String s) { return Base64.getUrlDecoder().decode(s); }

    public static void main(String[] args) throws Exception {
        String json = Files.readString(Path.of(args.length > 0 ? args[0] : "testdata/vectors.json"));
        AlgorithmParameters p = AlgorithmParameters.getInstance("EC");
        p.init(new ECGenParameterSpec("secp256r1"));
        PrivateKey priv = KeyFactory.getInstance("EC").generatePrivate(
            new ECPrivateKeySpec(new BigInteger(1, b64(field(json, "privateKey"))), p.getParameterSpec(ECParameterSpec.class)));
        byte[] pub = b64(field(json, "p256dh")), auth = b64(field(json, "auth"));
        Matcher c = Pattern.compile("\"enc\":\\s*\"([^\"]*)\",\\s*\"payload\":\\s*\"((?:[^\"\\\\]|\\\\.)*)\"").matcher(json);
        int n = 0;
        while (c.find()) {
            String want = c.group(2).replace("\\\"", "\"").replace("\\\\", "\\");
            want = new String(want.getBytes(StandardCharsets.UTF_8), StandardCharsets.UTF_8);
            String got = new String(PushCrypto.decrypt(b64(c.group(1)), priv, pub, auth), StandardCharsets.UTF_8);
            if (!got.equals(unescape(want))) throw new AssertionError("got " + got + ", want " + want);
            n++;
        }
        if (n < 3) throw new AssertionError("read " + n + " cases");

        // a tampered body must not decrypt
        String enc = field(json, "enc");
        byte[] bad = b64(enc);
        bad[bad.length - 1] ^= 1;
        try {
            PushCrypto.decrypt(bad, priv, pub, auth);
            throw new AssertionError("a tampered push decrypted");
        } catch (java.security.GeneralSecurityException expected) {
        }

        // a key pair made here gives the 65-byte point subscriptions send
        KeyPair kp = PushCrypto.newKeyPair();
        byte[] point = PushCrypto.publicKey((ECPublicKey) kp.getPublic());
        if (point.length != 65 || point[0] != 4) throw new AssertionError("bad public point");
        System.out.println("ok: " + n + " pushes from the server decrypted, tampering caught");
    }

    // JSON's \\uXXXX escapes (Go escapes < > & and may escape non-ASCII)
    static String unescape(String s) {
        Matcher m = Pattern.compile("\\\\u([0-9a-fA-F]{4})").matcher(s);
        StringBuilder b = new StringBuilder();
        while (m.find()) m.appendReplacement(b, Matcher.quoteReplacement(String.valueOf((char) Integer.parseInt(m.group(1), 16))));
        m.appendTail(b);
        return b.toString();
    }
}
