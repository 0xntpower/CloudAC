package sys

import (
	"log/slog"
	"time"
)

// Processor updates stored state from an incoming packet.
//
// Processors run before checks, and the ordering is load-bearing: a processor
// writes the state a check then reads. dispatch enforces it and holds one lock
// across both, so the pair cannot be torn apart by a concurrent message for the
// same player.
type Processor interface {
	Process(pkt *Packet, now time.Time, maxProfiles int)
}

// StatusProcessor maintains the profile store and each player's tracked state.
type StatusProcessor struct{}

func (StatusProcessor) Process(pkt *Packet, now time.Time, maxProfiles int) {
	switch pkt.Type {
	case packetJoin:
		if addProfile(pkt.UUID, now, maxProfiles) == nil {
			slog.Warn("profile capacity reached, ignoring join", "uuid", pkt.UUID, "tracked", profileCount())
			return
		}
		slog.Info("new profile", "uuid", pkt.UUID, "tracked", profileCount())

	case packetLeave:
		removeProfile(pkt.UUID)
		slog.Info("removed profile", "uuid", pkt.UUID, "tracked", profileCount())

	case packetFlying:
		prof, ok := getProfile(pkt.UUID)
		if !ok {
			return
		}
		prof.lastSeen = now

		prof.lastOnGround = prof.onGround
		prof.onGround = pkt.OnGround

		prof.lastServerOnGround = prof.serverOnGround
		prof.serverOnGround = pkt.ServerOnGround

		if pkt.HasPosition {
			prof.lastLocation.X = prof.latestLocation.X
			prof.lastLocation.Y = prof.latestLocation.Y
			prof.lastLocation.Z = prof.latestLocation.Z

			prof.latestLocation.X = pkt.X
			prof.latestLocation.Y = pkt.Y
			prof.latestLocation.Z = pkt.Z
		}

		if pkt.HasLook {
			prof.lastLocation.Yaw = prof.latestLocation.Yaw
			prof.lastLocation.Pitch = prof.latestLocation.Pitch

			prof.latestLocation.Yaw = pkt.Yaw
			prof.latestLocation.Pitch = pkt.Pitch
		}

	case packetClientAbilities:
		prof, ok := getProfile(pkt.UUID)
		if !ok {
			return
		}
		prof.lastSeen = now
		// Parsed with ParseBool at the wire boundary. This used to be a
		// substring test for "true", which matched "untrue" and missed "TRUE".
		prof.isFlyingClient = pkt.IsFlying
		prof.isAllowedFlightClient = pkt.CanFly

	case packetServerAbilities:
		prof, ok := getProfile(pkt.UUID)
		if !ok {
			return
		}
		prof.lastSeen = now
		prof.isFlyingServer = pkt.IsFlying
		prof.isAllowedFlightServer = pkt.CanFly
	}
}
