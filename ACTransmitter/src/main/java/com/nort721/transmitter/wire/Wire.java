package com.nort721.transmitter.wire;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;

/**
 * Versioned, authenticated framing for both directions of the CloudAC protocol.
 * <p>
 * A frame is {@code v1|<hmac-hex>|<payload>}. The MAC covers the payload only.
 * When no shared secret is configured the MAC section is empty, which is
 * permitted solely for a loopback deployment.
 * <p>
 * Three properties this replaces, all of which were absent:
 * <ul>
 *   <li>A version field. Both sides previously hardcoded positional indices
 *       while discarding parse errors, so a mismatched deployment produced
 *       silently wrong values rather than an error.</li>
 *   <li>Peer authentication. Both listeners accepted commands from anyone who
 *       could reach them, and on the verdict channel that meant kicking any
 *       player was a single line of netcat.</li>
 *   <li>An explicit charset. Streams were built with the platform default while
 *       the peer is UTF-8 by construction, so the wire encoding depended on
 *       which machine ran the build.</li>
 * </ul>
 */
public final class Wire {

    public static final String VERSION = "v1";

    /** Longest frame accepted. Beyond this the peer is malfunctioning or hostile. */
    public static final int MAX_FRAME_BYTES = 8192;

    private static final String HMAC_ALGORITHM = "HmacSHA256";
    private static final char[] HEX = "0123456789abcdef".toCharArray();

    private Wire() {
    }

    /** Wraps a payload in a versioned, authenticated frame. */
    public static String encode(byte[] secret, String payload) {
        return VERSION + "|" + mac(secret, payload) + "|" + payload;
    }

    /**
     * Verifies a frame and returns its payload, or {@code null} if the frame is
     * malformed, carries an unknown version, or fails authentication.
     * <p>
     * Returning null rather than throwing keeps the caller's error path a
     * simple branch. The previous code let an exception escape between the read
     * and the socket close, which leaked a descriptor on every malformed input.
     */
    public static String decode(byte[] secret, String line) {
        if (line == null || line.length() > MAX_FRAME_BYTES) {
            return null;
        }
        // Java's readLine already strips the terminator, but a peer may still
        // send a stray carriage return inside the frame.
        while (line.endsWith("\r") || line.endsWith("\n")) {
            line = line.substring(0, line.length() - 1);
        }

        int firstPipe = line.indexOf('|');
        if (firstPipe < 0) {
            return null;
        }
        if (!VERSION.equals(line.substring(0, firstPipe))) {
            return null;
        }
        int secondPipe = line.indexOf('|', firstPipe + 1);
        if (secondPipe < 0) {
            return null;
        }

        String presented = line.substring(firstPipe + 1, secondPipe);
        String payload = line.substring(secondPipe + 1);

        // Constant-time comparison. A byte-by-byte early-exit equals leaks how
        // much of a guessed MAC was correct.
        if (!MessageDigest.isEqual(
                presented.getBytes(StandardCharsets.UTF_8),
                mac(secret, payload).getBytes(StandardCharsets.UTF_8))) {
            return null;
        }
        return payload;
    }

    private static String mac(byte[] secret, String payload) {
        if (secret == null || secret.length == 0) {
            return "";
        }
        try {
            Mac hmac = Mac.getInstance(HMAC_ALGORITHM);
            hmac.init(new SecretKeySpec(secret, HMAC_ALGORITHM));
            return hex(hmac.doFinal(payload.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException | java.security.InvalidKeyException e) {
            // HmacSHA256 is required of every Java SE implementation, so this
            // is unreachable in practice. Failing closed is still the right
            // behaviour, because returning an empty MAC would silently disable
            // authentication.
            throw new IllegalStateException("HMAC-SHA256 unavailable", e);
        }
    }

    private static String hex(byte[] bytes) {
        char[] out = new char[bytes.length * 2];
        for (int i = 0; i < bytes.length; i++) {
            int v = bytes[i] & 0xFF;
            out[i * 2] = HEX[v >>> 4];
            out[i * 2 + 1] = HEX[v & 0x0F];
        }
        return new String(out);
    }

    /**
     * Strips characters that must never reach a console or a kick screen.
     * <p>
     * Removes C0 and C1 control characters, the section sign the Minecraft
     * client interprets as a formatting code, and the Unicode bidirectional
     * overrides that visually reverse the remainder of a line. All three
     * previously travelled from an unauthenticated socket straight into the
     * server log and the victim's kick screen.
     */
    public static String sanitize(String s, int maxLength) {
        if (s == null) {
            return "";
        }
        String trimmed = s.length() > maxLength ? s.substring(0, maxLength) : s;
        return trimmed.replaceAll("[\\p{Cntrl}\\u00A7\\u202A-\\u202E\\u2066-\\u2069]", "");
    }
}
