package com.sparkstonepdx.push;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.util.Log;
import androidx.core.app.NotificationCompat;
import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;
import java.util.Map;
import org.json.JSONObject;

/**
 * Receives FCM messages from the nook push app. An encrypted push ("enc") is decrypted here, so Google only ever
 * carried ciphertext; a plain one has its payload in "payload". If the app is open, the plugin gets it as
 * pushReceived; otherwise a payload with a title or body is shown as a notification, and tapping it opens the app with
 * pushOpened.
 */
public class PushService extends FirebaseMessagingService {
    static final String CHANNEL = "nook-push";
    static final String EXTRA = "com.sparkstonepdx.push.payload";

    @Override
    public void onNewToken(String token) {
        PushPlugin.tokenChanged(token);
    }

    @Override
    public void onMessageReceived(RemoteMessage msg) {
        Map<String, String> data = msg.getData();
        String payload;
        try {
            if (data.containsKey("enc")) {
                payload = PushKeys.load(this).open(data.get("enc"));
            } else if (data.containsKey("payload")) {
                payload = data.get("payload");
            } else {
                return; // not from the push app
            }
        } catch (Exception e) {
            Log.w("NookPush", "couldn't decrypt a push", e);
            return;
        }
        if (PushPlugin.received(payload)) return; // the app is open and has it
        show(this, payload);
    }

    static void show(Context ctx, String payload) {
        String title = "", body = "";
        try {
            JSONObject p = new JSONObject(payload);
            title = p.optString("title", "");
            body = p.optString("body", "");
        } catch (Exception ignored) {
        }
        if (title.isEmpty() && body.isEmpty()) return; // a data push: nothing to show
        NotificationManager nm = (NotificationManager) ctx.getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26 && nm.getNotificationChannel(CHANNEL) == null) {
            nm.createNotificationChannel(new NotificationChannel(CHANNEL, "Notifications", NotificationManager.IMPORTANCE_HIGH));
        }
        Intent open = ctx.getPackageManager().getLaunchIntentForPackage(ctx.getPackageName());
        if (open == null) return;
        open.putExtra(EXTRA, payload).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        int id = (int) (System.currentTimeMillis() & 0x7fffffff);
        PendingIntent tap = PendingIntent.getActivity(ctx, id, open, PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        int icon = ctx.getResources().getIdentifier("ic_stat_notify", "drawable", ctx.getPackageName());
        NotificationCompat.Builder n = new NotificationCompat.Builder(ctx, CHANNEL)
            .setSmallIcon(icon != 0 ? icon : ctx.getApplicationInfo().icon)
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(new NotificationCompat.BigTextStyle().bigText(body))
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setContentIntent(tap);
        nm.notify(id, n.build());
    }
}
