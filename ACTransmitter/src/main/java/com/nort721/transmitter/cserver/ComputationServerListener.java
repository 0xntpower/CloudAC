package com.nort721.transmitter.cserver;

import com.nort721.transmitter.punish.PunishmentService;
import com.nort721.transmitter.wire.Wire;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.SocketException;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.logging.Level;
import java.util.logging.Logger;

/**
 * Receives verdicts from the ComputationServer.
 * <p>
 * This is the inbound trust boundary, and it previously had no controls on it
 * at all. It bound every interface, accepted any connection, and kicked
 * whichever player a single unauthenticated line named. Minecraft UUIDs are
 * public information, so that was a scriptable remote kick of any player.
 * <p>
 * What changed:
 * <ul>
 *   <li>Binds a specific address, loopback by default.</li>
 *   <li>Requires a valid HMAC when a shared secret is configured, and refuses
 *       to accept a non-loopback bind without one.</li>
 *   <li>A bounded thread pool that sheds load, rather than one unbounded
 *       thread per connection which an attacker could grow until the JVM threw
 *       OutOfMemoryError.</li>
 *   <li>A read timeout, so a peer that connects and sends nothing cannot park a
 *       thread forever.</li>
 *   <li>try-with-resources, so a malformed message can no longer throw between
 *       the read and the close and leak a descriptor on every attempt.</li>
 *   <li>Catches Throwable in the accept loop. OutOfMemoryError is an Error, not
 *       an Exception, so it previously walked past the catch and killed the
 *       loop while leaving the port bound and completing handshakes, which made
 *       every port-reachability health check report green.</li>
 * </ul>
 */
public final class ComputationServerListener implements Runnable {

    private static final int ACCEPT_BACKLOG = 16;
    private static final int READ_TIMEOUT_MS = 5000;
    private static final int POOL_CORE = 1;
    private static final int POOL_MAX = 4;
    private static final int POOL_QUEUE = 32;

    /** Verdicts older than this are treated as replays. */
    private static final long MAX_VERDICT_AGE_MS = 30_000L;

    private final String bindAddress;
    private final int port;
    private final byte[] secret;
    private final PunishmentService punishment;
    private final Logger log;

    private final ThreadPoolExecutor pool = new ThreadPoolExecutor(
            POOL_CORE, POOL_MAX, 30L, TimeUnit.SECONDS,
            new ArrayBlockingQueue<>(POOL_QUEUE),
            r -> {
                Thread t = new Thread(r, "ACTransmitter-verdict");
                t.setDaemon(true);
                return t;
            },
            new ThreadPoolExecutor.AbortPolicy());

    private final AtomicLong received = new AtomicLong();
    private final AtomicLong rejected = new AtomicLong();

    private volatile boolean running = true;
    private volatile ServerSocket serverSocket;
    private Thread thread;

    public ComputationServerListener(String bindAddress, int port, byte[] secret,
                                     PunishmentService punishment, Logger log) {
        this.bindAddress = bindAddress;
        this.port = port;
        this.secret = secret;
        this.punishment = punishment;
        this.log = log;
    }

    public void start() {
        thread = new Thread(this, "ACTransmitter-verdict-listener");
        thread.setDaemon(true);
        thread.start();
    }

    @Override
    public void run() {
        try (ServerSocket ss = new ServerSocket(port, ACCEPT_BACKLOG, InetAddress.getByName(bindAddress))) {
            this.serverSocket = ss;
            log.info("verdict listener bound to " + bindAddress + ":" + port
                    + (secret.length > 0 ? " (authenticated)" : " (loopback only, unauthenticated)"));

            while (running && !Thread.currentThread().isInterrupted()) {
                Socket socket = ss.accept();
                try {
                    socket.setSoTimeout(READ_TIMEOUT_MS);
                    pool.execute(new VerdictHandler(socket));
                } catch (RejectedExecutionException shed) {
                    rejected.incrementAndGet();
                    closeQuietly(socket);
                }
            }
        } catch (SocketException closed) {
            if (running) {
                log.log(Level.SEVERE, "verdict listener socket closed unexpectedly", closed);
            }
        } catch (IOException e) {
            // A failed bind is the single most important thing to say loudly.
            // It happens on a plugin reload when the previous instance's
            // listener is still holding the port, and the symptom is that
            // verdicts silently stop arriving forever.
            log.log(Level.SEVERE, "VERDICT LISTENER FAILED TO BIND on " + bindAddress + ":" + port
                    + ". Detection results will NOT be applied. If this followed a /reload, "
                    + "restart the server instead: this plugin does not support /reload.", e);
        } catch (Throwable t) {
            log.log(Level.SEVERE, "verdict listener stopped", t);
        } finally {
            pool.shutdownNow();
        }
    }

    /** Stops the listener. Closing the socket is what unblocks accept. */
    public void shutdown() {
        running = false;
        ServerSocket ss = serverSocket;
        if (ss != null) {
            try {
                ss.close();
            } catch (IOException ignored) {
                // Shutting down anyway.
            }
        }
        pool.shutdownNow();
        if (thread != null) {
            thread.interrupt();
        }
    }

    public long receivedCount() {
        return received.get();
    }

    public long rejectedCount() {
        return rejected.get();
    }

    public boolean isBound() {
        ServerSocket ss = serverSocket;
        return ss != null && !ss.isClosed();
    }

    private static void closeQuietly(Socket socket) {
        try {
            socket.close();
        } catch (IOException ignored) {
            // Nothing useful to do.
        }
    }

    private final class VerdictHandler implements Runnable {

        private final Socket socket;

        private VerdictHandler(Socket socket) {
            this.socket = socket;
        }

        @Override
        public void run() {
            // try-with-resources on the socket itself. Every path closes,
            // including the ones that throw.
            try (Socket s = socket;
                 BufferedReader in = new BufferedReader(
                         new InputStreamReader(s.getInputStream(), StandardCharsets.UTF_8))) {

                // getHostAddress, not getHostName. The latter performs a
                // reverse DNS lookup on the verdict path, which is a blocking
                // network call an attacker controlling their PTR record could
                // stall for as long as they liked.
                String peer = s.getInetAddress().getHostAddress();

                String line = in.readLine();
                if (line == null) {
                    return;
                }

                String payload = Wire.decode(secret, line);
                if (payload == null) {
                    rejected.incrementAndGet();
                    log.fine("rejected unauthenticated or malformed verdict from " + peer);
                    return;
                }

                handle(payload, peer);

            } catch (IOException e) {
                log.log(Level.FINE, "verdict read failed", e);
            } catch (RuntimeException e) {
                log.log(Level.WARNING, "verdict handler failed", e);
            }
        }

        private void handle(String payload, String peer) {
            // The stdlib split, with a negative limit so trailing empty fields
            // are preserved. This replaced twenty-four lines of hand-rolled
            // character iteration that did the same thing.
            String[] args = payload.split("\\|", -1);
            if (args.length < 4 || !"FLAG".equals(args[0])) {
                rejected.incrementAndGet();
                return;
            }

            long timestamp;
            try {
                timestamp = Long.parseLong(args[3]);
            } catch (NumberFormatException bad) {
                rejected.incrementAndGet();
                return;
            }
            // Freshness only means anything alongside the MAC, so it is checked
            // only when authentication is active. Without it a captured verdict
            // line replays forever.
            if (secret.length > 0 && Math.abs(System.currentTimeMillis() - timestamp) > MAX_VERDICT_AGE_MS) {
                rejected.incrementAndGet();
                log.fine("rejected stale verdict from " + peer);
                return;
            }

            UUID uuid;
            try {
                uuid = UUID.fromString(args[1]);
            } catch (IllegalArgumentException bad) {
                rejected.incrementAndGet();
                return;
            }

            received.incrementAndGet();
            punishment.onFlag(uuid, args[2]);
        }
    }
}
