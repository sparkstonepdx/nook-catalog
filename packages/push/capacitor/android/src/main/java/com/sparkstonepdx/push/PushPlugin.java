package com.sparkstonepdx.push;

import android.Manifest;
import android.content.Intent;
import android.os.Build;
import com.getcapacitor.JSObject;
import com.getcapacitor.PermissionState;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import com.getcapacitor.annotation.Permission;
import com.getcapacitor.annotation.PermissionCallback;
import com.google.firebase.messaging.FirebaseMessaging;
import org.json.JSONObject;

/**
 * NookPush on Android: the FCM token and this device's push keys, for subscribing with the nook push app, and the
 * pushes it gets (decrypted in {@link PushService}) as pushReceived and pushOpened.
 */
@CapacitorPlugin(name = "NookPush", permissions = @Permission(strings = { Manifest.permission.POST_NOTIFICATIONS }, alias = "notifications"))
public class PushPlugin extends Plugin {
    private static PushPlugin active; // set while the app is open

    @Override
    public void load() {
        active = this;
        opened(getActivity().getIntent());
    }

    @Override
    protected void handleOnNewIntent(Intent intent) {
        super.handleOnNewIntent(intent);
        opened(intent);
    }

    @Override
    protected void handleOnDestroy() {
        if (active == this) active = null;
    }

    private void opened(Intent intent) {
        if (intent == null || !intent.hasExtra(PushService.EXTRA)) return;
        String payload = intent.getStringExtra(PushService.EXTRA);
        intent.removeExtra(PushService.EXTRA);
        notifyListeners("pushOpened", event(payload), true);
    }

    private static JSObject event(String payload) {
        JSObject e = new JSObject();
        try {
            e.put("payload", new JSObject(payload));
        } catch (Exception ex) {
            e.put("payload", payload);
        }
        return e;
    }

    /** A push arrived; true if the open app took it (so no notification is shown). */
    static boolean received(String payload) {
        PushPlugin p = active;
        if (p == null || p.getActivity() == null || !p.getBridge().getApp().isActive()) return false;
        p.notifyListeners("pushReceived", event(payload));
        return true;
    }

    static void tokenChanged(String token) {
        PushPlugin p = active;
        if (p != null) {
            JSObject e = new JSObject();
            e.put("token", token);
            p.notifyListeners("tokenChanged", e);
        }
    }

    @PluginMethod
    public void register(PluginCall call) {
        if (Build.VERSION.SDK_INT >= 33 && getPermissionState("notifications") != PermissionState.GRANTED) {
            requestPermissionForAlias("notifications", call, "afterPermission");
            return;
        }
        subscription(call);
    }

    @PermissionCallback
    private void afterPermission(PluginCall call) {
        if (getPermissionState("notifications") != PermissionState.GRANTED) {
            call.reject("notifications aren't allowed");
            return;
        }
        subscription(call);
    }

    private void subscription(PluginCall call) {
        PushKeys keys;
        try {
            keys = PushKeys.load(getContext());
        } catch (Exception e) {
            call.reject("couldn't make this device's push keys", e);
            return;
        }
        FirebaseMessaging.getInstance().getToken().addOnCompleteListener(task -> {
            if (!task.isSuccessful()) {
                call.reject("couldn't get an FCM token (is google-services.json in the app?)", task.getException());
                return;
            }
            JSObject k = new JSObject();
            k.put("p256dh", PushKeys.b64(keys.publicKey));
            k.put("auth", PushKeys.b64(keys.auth));
            JSObject out = new JSObject();
            out.put("kind", "fcm");
            out.put("token", task.getResult());
            out.put("keys", k);
            call.resolve(out);
        });
    }
}
