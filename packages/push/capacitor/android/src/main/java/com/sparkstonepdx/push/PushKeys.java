package com.sparkstonepdx.push;

import android.content.Context;
import android.content.SharedPreferences;
import android.util.Base64;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.PrivateKey;
import java.security.SecureRandom;
import java.security.interfaces.ECPublicKey;
import java.security.spec.PKCS8EncodedKeySpec;

/**
 * This device's push keys: a P-256 key pair and a 16-byte auth secret, made once and kept in the app's private
 * preferences, so the push service and the plugin read the same ones. The public half and the secret go to the push
 * app when subscribing; the private key never leaves the device.
 */
final class PushKeys {
    private static final String PREFS = "com.sparkstonepdx.push";
    final PrivateKey priv;
    final byte[] publicKey; // 65 bytes
    final byte[] auth;

    private PushKeys(PrivateKey priv, byte[] publicKey, byte[] auth) {
        this.priv = priv;
        this.publicKey = publicKey;
        this.auth = auth;
    }

    static String b64(byte[] b) {
        return Base64.encodeToString(b, Base64.URL_SAFE | Base64.NO_PADDING | Base64.NO_WRAP);
    }

    static byte[] unb64(String s) {
        return Base64.decode(s, Base64.URL_SAFE | Base64.NO_PADDING | Base64.NO_WRAP);
    }

    static synchronized PushKeys load(Context ctx) throws Exception {
        SharedPreferences p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        String priv = p.getString("private", null);
        if (priv != null) {
            PrivateKey k = KeyFactory.getInstance("EC").generatePrivate(new PKCS8EncodedKeySpec(unb64(priv)));
            return new PushKeys(k, unb64(p.getString("public", "")), unb64(p.getString("auth", "")));
        }
        KeyPair kp = PushCrypto.newKeyPair();
        byte[] auth = new byte[16];
        new SecureRandom().nextBytes(auth);
        byte[] pub = PushCrypto.publicKey((ECPublicKey) kp.getPublic());
        p.edit()
            .putString("private", b64(kp.getPrivate().getEncoded()))
            .putString("public", b64(pub))
            .putString("auth", b64(auth))
            .apply();
        return new PushKeys(kp.getPrivate(), pub, auth);
    }

    /** The payload of an encrypted push ("enc"), as the JSON text the sender sent. */
    String open(String enc) throws Exception {
        return new String(PushCrypto.decrypt(unb64(enc), priv, publicKey, auth), java.nio.charset.StandardCharsets.UTF_8);
    }
}
