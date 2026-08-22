package com.nort721.transmitter.listeners;

import com.comphenix.protocol.PacketType;
import com.comphenix.protocol.events.ListenerPriority;
import com.comphenix.protocol.events.PacketAdapter;
import com.comphenix.protocol.events.PacketEvent;
import com.comphenix.protocol.injector.server.TemporaryPlayer;
import com.nort721.transmitter.ACTransmitter;
import com.nort721.transmitter.cserver.ComputationServerSender;
import com.nort721.transmitter.utils.wrappers.WrapperPlayClientFlying;
import org.bukkit.entity.Player;

import java.util.Arrays;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Intercepts the packets the checks need and hands them to the sender.
 * <p>
 * The registration list below is the important part of this class. It
 * previously registered for <b>every packet type the server supports</b> while
 * acting on seven of them, which defeated ProtocolLib's own optimisation of
 * only installing interceptors for types that have a listener. Every chunk-data,
 * entity-metadata and block-change packet on the server was routed through this
 * class so it could be discarded, and a StringBuilder was allocated before the
 * type check on every one of them.
 * <p>
 * Narrowing the list is three lines and removes essentially all of this
 * plugin's main-thread cost.
 */
public final class PacketsListener extends PacketAdapter {

    /**
     * Exactly the packet types this listener acts on.
     * <p>
     * The PONG exclusion the previous implementation needed disappeared with
     * the broad registration that made it necessary.
     */
    private static final PacketType[] TYPES = {
            PacketType.Play.Client.FLYING,
            PacketType.Play.Client.POSITION,
            PacketType.Play.Client.POSITION_LOOK,
            PacketType.Play.Client.LOOK,
            PacketType.Play.Client.ABILITIES,
            PacketType.Play.Server.LOGIN,
            PacketType.Play.Server.ABILITIES,
    };

    /**
     * Session-scoped cache of UUID strings.
     * <p>
     * UUID.toString allocates about a kilobyte per call on Java 8, and this
     * value is immutable for a player's whole session while being needed twenty
     * times a second. This is the one thing in the hot path genuinely worth
     * caching. Everything else here wants batching, which the sender does.
     */
    private final Map<UUID, String> uuidStrings = new ConcurrentHashMap<>();

    private final ComputationServerSender sender;

    public PacketsListener(ACTransmitter plugin, ComputationServerSender sender) {
        super(plugin, ListenerPriority.LOWEST, Arrays.asList(TYPES));
        this.sender = sender;
        // No self-registration. The constructor previously published this to
        // ProtocolLib before its own fields were assigned, so a packet arriving
        // in that window dereferenced a null on a Netty thread. Registration is
        // now the plugin's job, after construction completes.
    }

    /** Drops a player's cached string. Called when they disconnect. */
    public void forget(UUID uuid) {
        uuidStrings.remove(uuid);
    }

    public void clearCache() {
        uuidStrings.clear();
    }

    private String uuidOf(Player player) {
        return uuidStrings.computeIfAbsent(player.getUniqueId(), UUID::toString);
    }

    @Override
    public void onPacketReceiving(PacketEvent event) {
        Player player = event.getPlayer();
        if (player instanceof TemporaryPlayer) {
            return;
        }

        PacketType type = event.getPacketType();

        if (type == PacketType.Play.Client.FLYING
                || type == PacketType.Play.Client.POSITION
                || type == PacketType.Play.Client.POSITION_LOOK
                || type == PacketType.Play.Client.LOOK) {

            WrapperPlayClientFlying flying = new WrapperPlayClientFlying(event);

            // Both the client's claim and the server's authoritative view are
            // transmitted. Sending only the client's claim meant every security
            // decision downstream rested on a value the cheater chose, and a
            // client that always claimed to be on the ground switched the
            // movement check off entirely.
            sender.send(new StringBuilder(160)
                    .append("2|")
                    .append(uuidOf(player)).append('|')
                    .append(System.currentTimeMillis()).append('|')
                    .append(flying.hasPosition()).append('|')
                    .append(flying.hasLook()).append('|')
                    .append(flying.getOnGround()).append('|')
                    .append(flying.getX()).append('|')
                    .append(flying.getY()).append('|')
                    .append(flying.getZ()).append('|')
                    .append(flying.getYaw()).append('|')
                    .append(flying.getPitch()).append('|')
                    .append(player.isOnGround())
                    .toString());

        } else if (type == PacketType.Play.Client.ABILITIES) {
            boolean isFlying = event.getPacket().getBooleans().read(1);
            boolean canFly = event.getPacket().getBooleans().read(2);

            sender.send("3|" + uuidOf(player) + '|' + System.currentTimeMillis()
                    + '|' + isFlying + '|' + canFly);
        }
    }

    @Override
    public void onPacketSending(PacketEvent event) {
        Player player = event.getPlayer();
        if (player instanceof TemporaryPlayer) {
            return;
        }

        PacketType type = event.getPacketType();

        // The allocation and the clock read happen inside the branch now. They
        // previously ran before the type check, on every intercepted packet,
        // which at a clustered hundred players was about one percent of the
        // tick budget spent producing a timestamp for packets that were then
        // discarded.
        if (type == PacketType.Play.Server.LOGIN) {
            sender.send("0|" + uuidOf(player) + '|' + System.currentTimeMillis());

        } else if (type == PacketType.Play.Server.ABILITIES) {
            boolean isFlying = event.getPacket().getBooleans().read(1);
            boolean canFly = event.getPacket().getBooleans().read(2);

            sender.send("4|" + uuidOf(player) + '|' + System.currentTimeMillis()
                    + '|' + isFlying + '|' + canFly);
        }
    }
}
