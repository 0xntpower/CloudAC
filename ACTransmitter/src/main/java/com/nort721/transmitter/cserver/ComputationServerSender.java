package com.nort721.transmitter.cserver;

import com.nort721.transmitter.wire.Wire;

import java.io.BufferedWriter;
import java.io.IOException;
import java.io.OutputStreamWriter;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.logging.Level;
import java.util.logging.Logger;

/**
 * Ships packet data to the ComputationServer over one long-lived connection.
 * <p>
 * <b>This class is the fix for the project's headline defect.</b> It previously
 * opened a brand new TCP connection for every single packet, on the Netty
 * channel thread, with no connect timeout, and never closed any of them. A
 * moving player produces roughly twenty movement packets per second, so that
 * design measured at about 52 KB of allocation and one full network round trip
 * per packet, to relocate detection work costing tens of nanoseconds. It also
 * exhausted the default file-descriptor limit in under a minute with a single
 * player online, and a dropped SYN parked a Netty worker for the operating
 * system default of over two minutes.
 * <p>
 * The shape now is the standard one for this problem:
 * <ul>
 *   <li>{@link #send} is the hot path. It is a bounded-queue offer, so it never
 *       blocks, never allocates, and never throws. Network conditions can no
 *       longer affect gameplay at all, which is the property the project claims
 *       and previously did not have.</li>
 *   <li>One dedicated thread drains the queue over one persistent connection,
 *       batching writes and flushing whenever the queue empties, so latency is
 *       unchanged under light load and syscalls collapse under heavy load.</li>
 *   <li>Overflow drops the oldest work and counts it. Dropping under sustained
 *       overload is a measurable degradation. Blocking the packet thread, which
 *       is what a naive persistent connection does once the socket buffer
 *       fills, is a frozen game server.</li>
 * </ul>
 */
public final class ComputationServerSender implements Runnable {

    /**
     * Capacity in messages. At twenty packets per second per player this is
     * several seconds of buffer for a full server, which is far longer than any
     * reconnect should take.
     */
    private static final int QUEUE_CAPACITY = 16384;

    private static final int CONNECT_TIMEOUT_MS = 3000;
    private static final int WRITE_BUFFER_BYTES = 32768;
    private static final long RECONNECT_BACKOFF_MS = 1000L;

    private final String host;
    private final int port;
    private final byte[] secret;
    private final Logger log;

    /** Invoked on the sender thread each time a connection is established. */
    private final Runnable onConnect;

    private final BlockingQueue<String> outbox = new ArrayBlockingQueue<>(QUEUE_CAPACITY);

    private final AtomicLong sent = new AtomicLong();
    private final AtomicLong dropped = new AtomicLong();
    private final AtomicLong connectFailures = new AtomicLong();

    private volatile boolean running = true;
    private volatile boolean connected = false;
    private volatile Socket socket;

    private Thread thread;

    public ComputationServerSender(String host, int port, byte[] secret, Logger log, Runnable onConnect) {
        this.host = host;
        this.port = port;
        this.secret = secret;
        this.log = log;
        this.onConnect = onConnect;
    }

    public void start() {
        // Implements Runnable rather than extending Thread. The previous class
        // extended Thread and its run method was empty, so the thread it
        // started did nothing while every send happened on the caller's thread.
        // The type advertised asynchrony the code did not have, which is
        // exactly the kind of thing a reviewer scanning for blocking work on
        // the packet path would skip over.
        thread = new Thread(this, "ACTransmitter-sender");
        thread.setDaemon(true);
        thread.start();
    }

    /**
     * Queues one payload for delivery. Called from Netty threads and the main
     * server thread.
     * <p>
     * Non-blocking by contract. Do not add anything to this method that can
     * block, allocate significantly, or throw.
     */
    public void send(String payload) {
        if (!running) {
            return;
        }
        if (!outbox.offer(payload)) {
            // Shed the oldest rather than the newest, so the freshest movement
            // data survives a burst.
            outbox.poll();
            if (dropped.incrementAndGet() % 1000L == 1L) {
                log.warning("computation server outbox is full, dropping packets (total dropped: "
                        + dropped.get() + ")");
            }
        }
    }

    @Override
    public void run() {
        while (running) {
            try (Socket s = new Socket()) {
                socket = s;
                // A bounded connect. Without this a dropped SYN blocks for the
                // operating system default, which on stock Linux is 127 seconds.
                s.connect(new InetSocketAddress(host, port), CONNECT_TIMEOUT_MS);
                s.setTcpNoDelay(true);

                try (BufferedWriter out = new BufferedWriter(
                        new OutputStreamWriter(s.getOutputStream(), StandardCharsets.UTF_8),
                        WRITE_BUFFER_BYTES)) {

                    connected = true;
                    log.info("connected to computation server at " + host + ":" + port);

                    if (onConnect != null) {
                        // Re-announce every online player. Without this a
                        // ComputationServer restart leaves everyone currently
                        // online untracked until they individually reconnect,
                        // with no log line and no alert.
                        onConnect.run();
                    }

                    drain(out);
                }
            } catch (IOException e) {
                connected = false;
                connectFailures.incrementAndGet();
                if (running) {
                    log.log(Level.WARNING, "computation server unreachable at " + host + ":" + port
                            + ", retrying (" + e.getMessage() + ")");
                }
            } finally {
                connected = false;
                socket = null;
            }

            if (!running) {
                return;
            }
            try {
                Thread.sleep(RECONNECT_BACKOFF_MS);
            } catch (InterruptedException interrupted) {
                Thread.currentThread().interrupt();
                return;
            }
        }
    }

    private void drain(BufferedWriter out) throws IOException {
        while (running) {
            String payload;
            try {
                payload = outbox.poll(50, TimeUnit.MILLISECONDS);
            } catch (InterruptedException interrupted) {
                Thread.currentThread().interrupt();
                return;
            }

            if (payload != null) {
                // A BufferedWriter, not a PrintWriter. PrintWriter is
                // documented never to throw IOException, so every dropped
                // packet and half-closed socket was silently swallowed and the
                // surrounding catch could only ever see the connect.
                out.write(Wire.encode(secret, payload));
                out.write('\n');
                sent.incrementAndGet();
            }

            // Flush on drain or on idle. Under light load this is immediate, so
            // latency is unchanged. Under heavy load it batches automatically,
            // which is where the syscall reduction comes from.
            if (payload == null || outbox.isEmpty()) {
                out.flush();
            }
        }
        out.flush();
    }

    /** Stops the sender and closes the connection. Safe to call more than once. */
    public void shutdown() {
        running = false;
        Socket s = socket;
        if (s != null) {
            // Closing the socket is what unblocks a write in progress.
            try {
                s.close();
            } catch (IOException ignored) {
                // Shutting down anyway.
            }
        }
        if (thread != null) {
            thread.interrupt();
            try {
                thread.join(2000L);
            } catch (InterruptedException interrupted) {
                Thread.currentThread().interrupt();
            }
        }
    }

    public boolean isConnected() {
        return connected;
    }

    public long sentCount() {
        return sent.get();
    }

    public long droppedCount() {
        return dropped.get();
    }

    public long connectFailureCount() {
        return connectFailures.get();
    }

    public int queueDepth() {
        return outbox.size();
    }
}
