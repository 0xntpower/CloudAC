package com.nort721.transmitter.listeners;

import com.nort721.transmitter.ACTransmitter;
import com.nort721.transmitter.cserver.ComputationServerSender;
import org.bukkit.entity.Player;
import org.bukkit.event.EventHandler;
import org.bukkit.event.EventPriority;
import org.bukkit.event.Listener;
import org.bukkit.event.player.PlayerQuitEvent;

/**
 * Emits the leave message and releases per-player caches.
 */
public final class BukkitListener implements Listener {

    private final ComputationServerSender sender;
    private final PacketsListener packets;

    public BukkitListener(ACTransmitter plugin, ComputationServerSender sender, PacketsListener packets) {
        this.sender = sender;
        this.packets = packets;
        plugin.getServer().getPluginManager().registerEvents(this, plugin);
    }

    @EventHandler(priority = EventPriority.MONITOR, ignoreCancelled = true)
    public void onPlayerQuit(PlayerQuitEvent event) {
        Player player = event.getPlayer();

        sender.send("1|" + player.getUniqueId() + '|' + System.currentTimeMillis());

        // Release the cached UUID string. Without this the cache grows for the
        // life of the server rather than for the life of a session.
        packets.forget(player.getUniqueId());
    }
}
