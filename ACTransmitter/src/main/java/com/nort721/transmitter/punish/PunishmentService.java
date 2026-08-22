package com.nort721.transmitter.punish;

import com.nort721.transmitter.wire.Wire;
import org.bukkit.Bukkit;
import org.bukkit.entity.Player;
import org.bukkit.plugin.Plugin;

import java.util.ArrayDeque;
import java.util.Deque;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicLong;
import java.util.logging.Logger;

/**
 * Decides what happens to a flagged player.
 * <p>
 * This exists because the punishment system was previously eight inline lines
 * in the socket handler with no policy of any kind. Two consequences followed
 * from that, and this class addresses both.
 * <p>
 * First, <b>there was no way to stop punishing without stopping a process.</b>
 * During a false-positive incident the only available lever was killing the
 * daemon, which also stops detection. {@link #setEnabled} separates the two, so
 * an operator can keep collecting evidence while nobody is being kicked.
 * <p>
 * Second, <b>nothing bounded the rate.</b> A tuning mistake in a check, which
 * is a realistic outcome of any change to the detection constants, would kick
 * every affected player as fast as verdicts arrived. The circuit breaker below
 * turns that from an unbounded incident into a bounded one that announces
 * itself.
 */
public final class PunishmentService {

    /** Kicks within the window above which the breaker opens. */
    private static final int BREAKER_THRESHOLD = 10;
    private static final long BREAKER_WINDOW_MS = 60_000L;

    /** Longest attacker-influenced reason shown to a player. */
    private static final int MAX_REASON_LENGTH = 64;

    private final Plugin plugin;
    private final Logger log;

    private volatile boolean enabled;
    private volatile boolean breakerOpen = false;

    private final Deque<Long> recentKicks = new ArrayDeque<>();

    private final AtomicLong kicks = new AtomicLong();
    private final AtomicLong suppressed = new AtomicLong();

    public PunishmentService(Plugin plugin, Logger log, boolean enabled) {
        this.plugin = plugin;
        this.log = log;
        this.enabled = enabled;
    }

    /**
     * Acts on a verdict. Safe to call from any thread.
     *
     * @param uuid   the flagged player
     * @param reason the check name, treated as untrusted input
     */
    public void onFlag(UUID uuid, String reason) {
        String safeReason = Wire.sanitize(reason, MAX_REASON_LENGTH);
        log.warning("flagged " + uuid + " (" + safeReason + ")");

        if (!enabled) {
            suppressed.incrementAndGet();
            return;
        }
        if (!allowKick()) {
            suppressed.incrementAndGet();
            return;
        }

        // The lookup happens inside the scheduled task, on the main thread.
        // Resolving the player on the socket thread and capturing the reference
        // touched a Bukkit collection the main thread was concurrently mutating,
        // and left a stale reference if the player disconnected in between.
        Bukkit.getScheduler().runTask(plugin, () -> {
            Player player = Bukkit.getPlayer(uuid);
            if (player == null) {
                return;
            }
            player.kickPlayer("Disconnected by anti-cheat");
            kicks.incrementAndGet();
        });
    }

    /**
     * Rate-limits kicks and opens a breaker if the rate is implausible.
     * <p>
     * An anti-cheat that misses a cheater loses a game. An anti-cheat that
     * kicks a stream of legitimate players loses a playerbase, so the failure
     * modes are not symmetric and only one of them needs a hard stop.
     */
    private synchronized boolean allowKick() {
        long now = System.currentTimeMillis();
        while (!recentKicks.isEmpty() && now - recentKicks.peekFirst() > BREAKER_WINDOW_MS) {
            recentKicks.pollFirst();
        }
        if (recentKicks.size() >= BREAKER_THRESHOLD) {
            if (!breakerOpen) {
                breakerOpen = true;
                log.severe("PUNISHMENT CIRCUIT BREAKER OPEN: " + recentKicks.size()
                        + " kicks within " + (BREAKER_WINDOW_MS / 1000) + " seconds. "
                        + "Kicks are suspended and detection continues. "
                        + "This usually means a check was retuned incorrectly. "
                        + "Investigate before re-enabling with /actransmitter punish on.");
            }
            return false;
        }
        if (breakerOpen) {
            breakerOpen = false;
            log.info("punishment circuit breaker closed, kicks resumed");
        }
        recentKicks.addLast(now);
        return true;
    }

    public void setEnabled(boolean value) {
        this.enabled = value;
        log.info("punishment is now " + (value ? "enabled" : "disabled"));
    }

    public boolean isEnabled() {
        return enabled;
    }

    public boolean isBreakerOpen() {
        return breakerOpen;
    }

    public long kickCount() {
        return kicks.get();
    }

    public long suppressedCount() {
        return suppressed.get();
    }
}
