package com.nort721.transmitter;

import com.comphenix.protocol.ProtocolLibrary;
import com.nort721.transmitter.cserver.ComputationServerListener;
import com.nort721.transmitter.cserver.ComputationServerSender;
import com.nort721.transmitter.listeners.BukkitListener;
import com.nort721.transmitter.listeners.PacketsListener;
import com.nort721.transmitter.punish.PunishmentService;
import org.bukkit.Bukkit;
import org.bukkit.ChatColor;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.entity.Player;
import org.bukkit.plugin.java.JavaPlugin;
import org.bukkit.scheduler.BukkitTask;

import java.net.InetAddress;
import java.net.UnknownHostException;
import java.nio.charset.StandardCharsets;

/**
 * Plugin entry point.
 * <p>
 * Two things here are worth reading before changing anything.
 * <p>
 * <b>Startup order is load-bearing.</b> The sender is constructed and started
 * before the packet listener is registered. The previous order registered the
 * listener first and assigned the sender five lines later, so a packet arriving
 * in that window dereferenced a null field on a Netty thread. On a cold start
 * nobody is connected and the window is empty, which is why it was never seen.
 * <p>
 * <b>onDisable must undo everything onEnable did.</b> The listener thread and
 * its ServerSocket were previously never stopped, so a reload left the old
 * thread holding the port, the new bind threw, and verdicts silently stopped
 * arriving with nothing in the log to say detection had ended.
 */
public final class ACTransmitter extends JavaPlugin {

    private static final long HEARTBEAT_TICKS = 20L * 30L;

    private static ACTransmitter instance;

    private ComputationServerSender sender;
    private ComputationServerListener verdictListener;
    private PacketsListener packetsListener;
    private PunishmentService punishment;
    private BukkitTask heartbeat;

    /**
     * Returns the running plugin instance.
     * <p>
     * A field assigned in onEnable, not a JavaPlugin.getPlugin lookup. The
     * lookup walks the plugin manager on every call and throws rather than
     * returning null once the plugin is unloading, which is exactly when a
     * verdict may still be in flight.
     */
    public static ACTransmitter getInstance() {
        return instance;
    }

    @Override
    public void onEnable() {
        instance = this;
        saveDefaultConfig();

        String host = getConfig().getString("computation-server.host", "127.0.0.1");
        int port = getConfig().getInt("computation-server.port", 1234);
        String bind = getConfig().getString("verdict-listener.bind-address", "127.0.0.1");
        int listenPort = getConfig().getInt("verdict-listener.port", 1212);
        String secretText = getConfig().getString("shared-secret", "");
        boolean punishEnabled = getConfig().getBoolean("punishment.enabled", true);

        byte[] secret = secretText == null ? new byte[0] : secretText.getBytes(StandardCharsets.UTF_8);

        if (!validateNetworkConfig(secret, host, bind)) {
            getLogger().severe("ACTransmitter is disabling itself. Fix config.yml and restart.");
            getServer().getPluginManager().disablePlugin(this);
            return;
        }

        punishment = new PunishmentService(this, getLogger(), punishEnabled);

        verdictListener = new ComputationServerListener(bind, listenPort, secret, punishment, getLogger());
        verdictListener.start();

        // Constructed and started before anything can call it.
        sender = new ComputationServerSender(host, port, secret, getLogger(), this::announceOnlinePlayers);
        sender.start();

        packetsListener = new PacketsListener(this, sender);
        ProtocolLibrary.getProtocolManager().addPacketListener(packetsListener);

        new BukkitListener(this, sender, packetsListener);

        heartbeat = getServer().getScheduler().runTaskTimerAsynchronously(
                this, this::logHeartbeat, HEARTBEAT_TICKS, HEARTBEAT_TICKS);

        sendConsoleMessage("has been enabled");
    }

    @Override
    public void onDisable() {
        if (heartbeat != null) {
            heartbeat.cancel();
        }
        if (packetsListener != null) {
            ProtocolLibrary.getProtocolManager().removePacketListeners(this);
            packetsListener.clearCache();
        }
        if (verdictListener != null) {
            verdictListener.shutdown();
        }
        if (sender != null) {
            sender.shutdown();
        }
        instance = null;
        sendConsoleMessage("has been disabled");
    }

    /**
     * Refuses a configuration that would expose an unauthenticated port.
     * <p>
     * Without a shared secret, anyone able to reach the verdict port can kick
     * any player, and anyone able to reach the computation server can forge
     * packet data. Both are refused at startup rather than warned about,
     * because a warning in a Minecraft console scrolls away in seconds.
     */
    private boolean validateNetworkConfig(byte[] secret, String host, String bind) {
        if (secret.length > 0) {
            if (secret.length < 16) {
                getLogger().severe("shared-secret must be at least 16 characters.");
                return false;
            }
            return true;
        }
        if (!isLoopback(bind)) {
            getLogger().severe("Refusing to bind the verdict listener to " + bind
                    + " without a shared-secret. Anyone who can reach that port could kick any player. "
                    + "Set verdict-listener.bind-address to 127.0.0.1, or configure shared-secret.");
            return false;
        }
        if (!isLoopback(host)) {
            getLogger().severe("Refusing to send packet data to " + host
                    + " without a shared-secret. It would cross the network unauthenticated and in cleartext. "
                    + "Set computation-server.host to 127.0.0.1, or configure shared-secret.");
            return false;
        }
        return true;
    }

    private boolean isLoopback(String host) {
        if (host == null || host.isEmpty()) {
            return false;
        }
        try {
            return InetAddress.getByName(host).isLoopbackAddress();
        } catch (UnknownHostException e) {
            return false;
        }
    }

    /**
     * Re-announces every online player to the ComputationServer.
     * <p>
     * Called on every successful connect. Without it, a ComputationServer
     * restart leaves everyone currently online untracked: joins are only
     * emitted from the server login packet, which already fired for them, so
     * detection stays off for the entire current playerbase until each player
     * individually reconnects.
     */
    private void announceOnlinePlayers() {
        long now = System.currentTimeMillis();
        for (Player player : Bukkit.getOnlinePlayers()) {
            sender.send("0|" + player.getUniqueId() + '|' + now);
        }
    }

    /**
     * Reports progress so a stalled pipeline is visible.
     * <p>
     * Before this existed, the only sign the system worked end to end was a
     * kick, and kicks are by design rare. A quiet console was the expected
     * output of a healthy anti-cheat and of a completely dead one.
     */
    private void logHeartbeat() {
        boolean listenerBound = verdictListener != null && verdictListener.isBound();
        boolean senderUp = sender != null && sender.isConnected();

        getLogger().info(String.format(
                "heartbeat sent=%d dropped=%d queue=%d connected=%s listener_bound=%s "
                        + "verdicts=%d rejected=%d kicks=%d suppressed=%d punish=%s",
                sender == null ? 0 : sender.sentCount(),
                sender == null ? 0 : sender.droppedCount(),
                sender == null ? 0 : sender.queueDepth(),
                senderUp, listenerBound,
                verdictListener == null ? 0 : verdictListener.receivedCount(),
                verdictListener == null ? 0 : verdictListener.rejectedCount(),
                punishment == null ? 0 : punishment.kickCount(),
                punishment == null ? 0 : punishment.suppressedCount(),
                punishment != null && punishment.isEnabled()));

        if (!senderUp && !Bukkit.getOnlinePlayers().isEmpty()) {
            getLogger().severe("NOT CONNECTED to the computation server while players are online: "
                    + "no detection is happening.");
        }
        if (!listenerBound) {
            getLogger().severe("verdict listener is not bound: detection results cannot be applied.");
        }
    }

    @Override
    public boolean onCommand(CommandSender caller, Command command, String label, String[] args) {
        if (args.length == 0) {
            caller.sendMessage(ChatColor.RED + "Usage: /" + label + " <status|punish on|punish off>");
            return true;
        }
        if (args[0].equalsIgnoreCase("status")) {
            caller.sendMessage(ChatColor.GRAY + "Sender connected: " + sender.isConnected());
            caller.sendMessage(ChatColor.GRAY + "Sent: " + sender.sentCount()
                    + ", dropped: " + sender.droppedCount() + ", queued: " + sender.queueDepth());
            caller.sendMessage(ChatColor.GRAY + "Verdict listener bound: " + verdictListener.isBound());
            caller.sendMessage(ChatColor.GRAY + "Verdicts: " + verdictListener.receivedCount()
                    + ", rejected: " + verdictListener.rejectedCount());
            caller.sendMessage(ChatColor.GRAY + "Punishment enabled: " + punishment.isEnabled()
                    + ", breaker open: " + punishment.isBreakerOpen());
            return true;
        }
        if (args[0].equalsIgnoreCase("punish") && args.length >= 2) {
            // The kill switch. During a false-positive incident this is the
            // difference between a several-second outage and stopping a process.
            boolean on = args[1].equalsIgnoreCase("on");
            punishment.setEnabled(on);
            caller.sendMessage(ChatColor.GREEN + "Punishment " + (on ? "enabled" : "disabled") + ".");
            return true;
        }
        caller.sendMessage(ChatColor.RED + "Usage: /" + label + " <status|punish on|punish off>");
        return true;
    }

    public void sendConsoleMessage(String msg) {
        getLogger().info(msg);
    }

    public ComputationServerSender getComputationServerSender() {
        return sender;
    }

    public PunishmentService getPunishment() {
        return punishment;
    }
}
