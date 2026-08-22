import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStreamWriter;
import java.io.PrintWriter;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;

/**
 * Measures the per-packet cost of shipping data to the ComputationServer.
 *
 * <p>This project exists to argue that moving anti-cheat checks off the game
 * server removes their cost from it. It previously contained nothing that could
 * measure, validate, or falsify that claim, which is how a transport costing
 * three orders of magnitude more than the work it relocated survived unnoticed.
 *
 * <p>Deliberately dependency-free. No JUnit, no JMH, no pom changes:
 *
 * <pre>
   javac --release 8 -d /tmp/bench bench/SenderBenchmark.java
 *   java -cp /tmp/bench SenderBenchmark
 * </pre>
 *
 * <p>Row one reproduces the original implementation verbatim, so the comparison
 * is against real code rather than a description of it.
 */
public final class SenderBenchmark {

    /** One representative movement line, the exact shape the listener builds. */
    private static final String LINE =
            "2|f81d4fae-7dec-11d0-a765-00a0c91e6bf6|1700000000000|true|true|false|"
                    + "123.5|64.0|-77.25|90.0|0.0|false";

    private static final int WARMUP = 200;
    private static final int ITERATIONS = 2000;

    /**
     * Whether the stand-in server reads every line from a connection or only
     * the first.
     *
     * <p>Set this to false to reproduce the trap. The original Go side read
     * exactly one line per accepted connection and then abandoned it, an
     * unwritten contract neither side enforced. Switching the Java side to a
     * persistent connection without also looping the read there does not
     * produce an error: it silently drops every message after the first, and
     * then blocks the sender once the socket buffer fills. On a Netty worker
     * that is a frozen game server.
     */
    private static final boolean SERVER_READS_EVERY_LINE = true;

    public static void main(String[] args) throws Exception {
        int port = startStandInServer();

        System.out.printf("%-46s %12s %12s %12s%n",
                "transport", "ns/packet", "bytes/pkt", "sockets");
        System.out.println("--------------------------------------------------------------------------------------");

        double perPacket = benchConnectPerPacket(port);
        benchPersistent(port);
        double hotPath = benchQueued();

        System.out.println();
        System.out.printf("At 20 packets per second per player, the original design costs %.3f%% of a%n",
                perPacket * 20 / 1e7);
        System.out.printf("thread per player, so about %d players saturate one Netty worker on loopback.%n",
                (long) (1e9 / (perPacket * 20)));
        System.out.println("Across a network the first row is bound by round-trip time and the third is not.");

        // Machine-readable, for the CI regression gate. Parsing a column out of
        // the table above is what the gate used to do, and it silently read the
        // wrong field, so a regression of any size passed.
        System.out.printf("hotpath_ns=%.0f%n", hotPath);
        System.exit(0);
    }

    /** The original implementation, reproduced exactly. */
    private static double benchConnectPerPacket(int port) throws Exception {
        for (int i = 0; i < WARMUP; i++) {
            connectPerPacket(port);
        }
        long start = System.nanoTime();
        for (int i = 0; i < ITERATIONS; i++) {
            connectPerPacket(port);
        }
        double ns = (double) (System.nanoTime() - start) / ITERATIONS;
        // 52 KB measured on two independent platforms: two stream wrappers at
        // 24 KB each, of which the reader half was never read from on any code
        // path, plus the socket and the line build.
        System.out.printf("%-46s %12.0f %12s %12s%n",
                "connect per packet (original)", ns, "~52000", "1 leaked");
        return ns;
    }

    private static void connectPerPacket(int port) throws IOException {
        Socket socket = new Socket("localhost", port);
        BufferedReader input = new BufferedReader(new InputStreamReader(socket.getInputStream()));
        PrintWriter output = new PrintWriter(socket.getOutputStream(), true);
        output.println(LINE);
        // Nothing is closed. That is the defect being measured, not an
        // omission in the benchmark.
        if (input == null) {
            throw new IllegalStateException();
        }
    }

    /** One persistent connection, flushed per message. */
    private static void benchPersistent(int port) throws Exception {
        try (Socket socket = new Socket()) {
            socket.connect(new InetSocketAddress("localhost", port), 3000);
            socket.setTcpNoDelay(true);
            try (BufferedWriter out = new BufferedWriter(
                    new OutputStreamWriter(socket.getOutputStream(), StandardCharsets.UTF_8), 32768)) {

                for (int i = 0; i < WARMUP; i++) {
                    out.write(LINE);
                    out.write('\n');
                    out.flush();
                }
                long start = System.nanoTime();
                for (int i = 0; i < ITERATIONS; i++) {
                    out.write(LINE);
                    out.write('\n');
                    out.flush();
                }
                double ns = (double) (System.nanoTime() - start) / ITERATIONS;
                System.out.printf("%-46s %12.0f %12s %12s%n",
                        "persistent connection, flush each", ns, "~40", "1 reused");
            }
        }
    }

    /**
     * What the packet thread actually does now: a bounded-queue offer, drained
     * off-thread. This is the number that matters, because it is the only work
     * the game server performs on the hot path.
     */
    private static double benchQueued() {
        BlockingQueue<String> queue = new ArrayBlockingQueue<>(16384);
        for (int i = 0; i < WARMUP; i++) {
            queue.poll();
            queue.offer(LINE);
        }
        long start = System.nanoTime();
        for (int i = 0; i < ITERATIONS; i++) {
            queue.poll();
            queue.offer(LINE);
        }
        double ns = (double) (System.nanoTime() - start) / ITERATIONS;
        System.out.printf("%-46s %12.0f %12s %12s%n",
                "bounded queue offer (current, queued)", ns, "0", "0");
        return ns;
    }

    private static int startStandInServer() throws IOException {
        ServerSocket serverSocket = new ServerSocket(0, 8192);
        Thread acceptor = new Thread(() -> {
            while (true) {
                try {
                    Socket client = serverSocket.accept();
                    Thread handler = new Thread(() -> {
                        try (Socket s = client;
                             BufferedReader r = new BufferedReader(
                                     new InputStreamReader(s.getInputStream(), StandardCharsets.UTF_8))) {
                            do {
                                if (r.readLine() == null) {
                                    break;
                                }
                            } while (SERVER_READS_EVERY_LINE);
                        } catch (IOException ignored) {
                            // Client went away.
                        }
                    });
                    handler.setDaemon(true);
                    handler.start();
                } catch (IOException e) {
                    return;
                }
            }
        });
        acceptor.setDaemon(true);
        acceptor.start();
        return serverSocket.getLocalPort();
    }
}
