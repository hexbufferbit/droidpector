package com.apkinspector.testapp;

import android.app.Activity;
import android.os.Bundle;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.security.SecureRandom;

import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

/**
 * droidpector TestApp: every button issues one well-known request against
 * the deterministic test server so end-to-end tests can assert on captured
 * traffic. The base URL can be overridden with the "base" intent extra, and an
 * "action" extra triggers a button automatically.
 */
public class MainActivity extends Activity {
    static final String DEFAULT_BASE = "https://test.apkinspector.internal";
    static final String WS_HOST = "test.apkinspector.internal";

    private TextView status;
    private String base = DEFAULT_BASE;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        String b = getIntent().getStringExtra("base");
        if (b != null && b.length() > 0) {
            base = b;
        }
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(32, 32, 32, 32);
        TextView title = new TextView(this);
        title.setText("droidpector TestApp");
        title.setTextSize(22);
        root.addView(title);
        addButton(root, "GET", "get");
        addButton(root, "POST", "post");
        addButton(root, "JSON", "json");
        addButton(root, "Image", "image");
        addButton(root, "Error", "error");
        addButton(root, "WebSocket", "ws");
        status = new TextView(this);
        status.setText("Ready");
        root.addView(status);
        ScrollView scroll = new ScrollView(this);
        scroll.addView(root);
        // Every touch is logged with its logical coordinates so tests can
        // verify how host pointer input maps into the rotated display.
        scroll.setOnTouchListener(new View.OnTouchListener() {
            @Override
            public boolean onTouch(View v, android.view.MotionEvent ev) {
                android.util.Log.i("droidpector-touch", "action=" + ev.getActionMasked() + " x=" + (int) ev.getRawX() + " y=" + (int) ev.getRawY());
                return false;
            }
        });
        setContentView(scroll);

        String action = getIntent().getStringExtra("action");
        if (action != null) {
            run(action);
        }
    }

    private void addButton(LinearLayout root, String label, final String action) {
        Button button = new Button(this);
        button.setText(label);
        button.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                run(action);
            }
        });
        root.addView(button);
    }

    private void show(final String text) {
        runOnUiThread(new Runnable() {
            @Override
            public void run() {
                status.setText(text);
            }
        });
    }

    private void run(final String action) {
        show("Running " + action + "...");
        new Thread(new Runnable() {
            @Override
            public void run() {
                try {
                    String result;
                    if ("get".equals(action)) {
                        result = http("GET", "/test/get?source=testapp", null, null);
                    } else if ("post".equals(action)) {
                        result = http("POST", "/test/post", "application/json", "{\"username\":\"testapp\",\"password\":\"secret\"}");
                    } else if ("json".equals(action)) {
                        result = http("GET", "/test/json", null, null);
                    } else if ("image".equals(action)) {
                        result = http("GET", "/test/image", null, null);
                    } else if ("error".equals(action)) {
                        result = http("GET", "/test/error", null, null);
                    } else if ("ws".equals(action)) {
                        result = websocket();
                    } else {
                        result = "unknown action " + action;
                    }
                    show(action + ": " + result);
                } catch (Exception e) {
                    show(action + " failed: " + e);
                }
            }
        }).start();
    }

    private String http(String method, String path, String contentType, String body) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(base + path).openConnection();
        c.setRequestMethod(method);
        c.setConnectTimeout(15000);
        c.setReadTimeout(15000);
        c.setRequestProperty("X-TestApp", "1");
        if (body != null) {
            c.setDoOutput(true);
            c.setRequestProperty("Content-Type", contentType);
            OutputStream os = c.getOutputStream();
            os.write(body.getBytes("UTF-8"));
            os.close();
        }
        int code = c.getResponseCode();
        InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
        int n = 0;
        if (in != null) {
            byte[] buf = new byte[8192];
            int r;
            while ((r = in.read(buf)) > 0) {
                n += r;
            }
            in.close();
        }
        c.disconnect();
        return "HTTP " + code + " (" + n + " bytes)";
    }

    /** Minimal RFC 6455 client: handshake, one text message, read the echo. */
    private String websocket() throws Exception {
        SSLSocket s = (SSLSocket) SSLSocketFactory.getDefault().createSocket(WS_HOST, 443);
        s.setSoTimeout(15000);
        byte[] key = new byte[16];
        new SecureRandom().nextBytes(key);
        String k = java.util.Base64.getEncoder().encodeToString(key);
        OutputStream out = s.getOutputStream();
        out.write(("GET /test/ws HTTP/1.1\r\nHost: " + WS_HOST + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                + "Sec-WebSocket-Key: " + k + "\r\nSec-WebSocket-Version: 13\r\n\r\n").getBytes("UTF-8"));
        out.flush();
        InputStream in = s.getInputStream();
        String headers = readHeaders(in);
        if (!headers.startsWith("HTTP/1.1 101")) {
            s.close();
            return "upgrade failed: " + headers;
        }
        String welcome = readFrame(in);
        byte[] msg = "hello from testapp".getBytes("UTF-8");
        byte[] mask = {1, 2, 3, 4};
        ByteArrayOutputStream f = new ByteArrayOutputStream();
        f.write(0x81);
        f.write(0x80 | msg.length);
        f.write(mask);
        for (int i = 0; i < msg.length; i++) {
            f.write(msg[i] ^ mask[i % 4]);
        }
        out.write(f.toByteArray());
        out.flush();
        String echo = readFrame(in);
        s.close();
        return "ws " + welcome + " / " + echo;
    }

    private static String readHeaders(InputStream in) throws Exception {
        ByteArrayOutputStream b = new ByteArrayOutputStream();
        int state = 0;
        while (state < 4) {
            int c = in.read();
            if (c < 0) {
                break;
            }
            b.write(c);
            state = (c == '\r' && (state == 0 || state == 2)) || (c == '\n' && (state == 1 || state == 3)) ? state + 1 : 0;
        }
        return b.toString("UTF-8");
    }

    private static String readFrame(InputStream in) throws Exception {
        in.read();
        int len = in.read() & 0x7f;
        if (len == 126) {
            len = (in.read() << 8) | in.read();
        }
        byte[] data = new byte[len];
        int off = 0;
        while (off < len) {
            int r = in.read(data, off, len - off);
            if (r < 0) {
                break;
            }
            off += r;
        }
        return new String(data, 0, off, "UTF-8");
    }
}
