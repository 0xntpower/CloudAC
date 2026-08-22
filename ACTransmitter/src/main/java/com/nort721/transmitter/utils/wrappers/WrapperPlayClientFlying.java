package com.nort721.transmitter.utils.wrappers;

import com.comphenix.protocol.events.PacketContainer;
import com.comphenix.protocol.events.PacketEvent;

/**
 * Read-only view over a client movement packet.
 * <p>
 * <b>Every accessor here reads by ordinal index into the underlying NMS
 * class.</b> Those indices are positions in Mojang's field ordering, not names,
 * and Mojang reorders and adds fields between Minecraft versions. A shifted
 * index does not throw, it silently returns the wrong boolean or the wrong
 * double, which downstream means the movement check evaluates garbage and kicks
 * real players.
 * <p>
 * <b>Re-derive and re-verify every index below before targeting a different
 * Minecraft version.</b> This class is the single largest risk in any version
 * port, and it is invisible in a version-bump diff.
 * <p>
 * There is deliberately no setter. This plugin never modifies packets in
 * flight, which is what makes it silent, and offering a mutator would advertise
 * a capability the design does not want.
 */
public final class WrapperPlayClientFlying {

    private final PacketContainer packetData;

    public WrapperPlayClientFlying(PacketEvent packetEvent) {
        packetData = packetEvent.getPacket();
    }

    /**
     * @return true if the packet carries a position.
     */
    public boolean hasPosition() {
        return packetData.getBooleans().read(1);
    }

    /**
     * @return true if the packet carries a look direction.
     */
    public boolean hasLook() {
        return packetData.getBooleans().read(2);
    }

    /**
     * Returns the client's own claim about whether it is standing on the ground.
     * <p>
     * This is an assertion by a potentially hostile party. Never make a
     * security decision on it alone. Compare it against the server's view from
     * {@code Player#isOnGround()}, which is what the wire format transmits
     * alongside it.
     *
     * @return the client's claimed ground state.
     */
    public boolean getOnGround() {
        return packetData.getBooleans().read(0);
    }

    public double getX() {
        return packetData.getDoubles().read(0);
    }

    public double getY() {
        return packetData.getDoubles().read(1);
    }

    public double getZ() {
        return packetData.getDoubles().read(2);
    }

    public float getYaw() {
        return packetData.getFloat().read(0);
    }

    public float getPitch() {
        return packetData.getFloat().read(1);
    }
}
