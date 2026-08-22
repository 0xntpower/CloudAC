package sys

import "math"

// Verdict is a check's decision about a player.
//
// Checks return verdicts. They do not deliver them. The previous design had the
// check layer dial a TCP socket with a hardcoded address, which is why an
// unhandled nil connection in the delivery path lived inside the detection
// logic instead of in a transport layer where it would have been obvious.
type Verdict struct {
	UUID  string
	Check string
}

// Check inspects a packet against a player's tracked state.
//
// A nil profile is never passed. dispatch resolves the profile first and skips
// the check layer entirely when the player is unknown.
type Check interface {
	Name() string
	Inspect(pkt *Packet, prof *Profile) bool
}

// CheckAbilities flags a client that claims a flight permission the server
// never granted it.
//
// KNOWN LIMITATION, and it is the ceiling on this check rather than a bug in
// it. isAllowedFlightClient comes from the client's own abilities packet. A
// cheat client that flies by sending movement packets, and either reports
// canFly as false or omits the abilities packet altogether, never trips this.
// The check catches a client honest enough to announce a capability it was not
// given. Closing that gap needs the server's authoritative view transmitted
// alongside the client's claim, and a comparison between the two.
type CheckAbilities struct{}

func (CheckAbilities) Name() string { return "CheckAbilities" }

func (CheckAbilities) Inspect(pkt *Packet, prof *Profile) bool {
	if pkt.Type != packetClientAbilities {
		return false
	}
	bad := prof.isAllowedFlightClient && !prof.isAllowedFlightServer
	if !bad {
		// Latch clears when the condition does, so a later genuine occurrence
		// still reports.
		prof.abilitiesFlagged = false
		return false
	}
	if prof.abilitiesFlagged {
		// Already reported. Without this latch the check fires on every
		// abilities packet for as long as the condition holds, which is a kick
		// on every packet rather than a kick on the offence.
		return false
	}
	prof.abilitiesFlagged = true
	return true
}

// Speed check tuning.
//
// WARNING, and this must be read before any of these numbers is trusted.
// Until the value-versus-pointer defect in the profile store was fixed, this
// check's state was discarded on every packet and its flag branch was
// unreachable, so none of these constants had ever executed against real
// movement. They are a first pass chosen to separate the fastest legitimate
// airborne movement from an obvious speed cheat, and they need validation
// against recorded traces from real players before anyone relies on them.
//
// The measured separation they are based on, in blocks per tick:
//
//	walking                     0.2158
//	sprinting                   0.2806
//	sprint-jump arc             0.3300
//	sprinting with Speed II     0.3648
//	legacy sprint-jump peak     0.4000
//
// For steady airborne movement at v blocks per tick the excess below works out
// at 0.09*v, so the fastest legitimate case above produces 0.036 and the
// threshold of 0.08 leaves roughly a factor of two of headroom.
const (
	// airFriction is Minecraft's per-tick horizontal drag multiplier. It
	// applies to LINEAR velocity. The previous code multiplied it against a
	// SQUARED distance, which is dimensionally meaningless and made the
	// effective threshold scale with the square of speed.
	airFriction = 0.91

	// speedExcessThreshold is how far beyond the friction prediction a single
	// tick may travel before it counts as a violation, in blocks.
	speedExcessThreshold = 0.08

	// speedViolationsToFlag is how many CONSECUTIVE violating packets are
	// required. Any conforming packet resets the counter to zero, so this
	// measures an unbroken run and a single anomalous tick cannot flag.
	speedViolationsToFlag = 3

	// speedSettleTicks is how many airborne packets to observe before
	// evaluating. The tick a player leaves the ground compares an airborne
	// distance against a grounded one, which reads as acceleration and is not.
	speedSettleTicks = 3

	// tickMillis is one Minecraft tick. Used only to convert a measured time
	// delta into ticks, never assumed.
	tickMillis = 50.0

	// maxPlausibleGapMillis bounds the time delta. A larger gap means lag, a
	// reconnect, or a withheld burst, and predicting across it is meaningless.
	maxPlausibleGapMillis = 1000.0
)

// CheckSpeed compares horizontal displacement against what air drag alone
// permits.
//
// It evaluates only while the player is airborne according to the GAME SERVER.
// Gating on the client's own onGround field, as this used to, meant a client
// that always claimed to be grounded never entered the check at all.
//
// KNOWN LIMITATIONS, inherited from the model rather than introduced here.
//
// First, a friction-prediction model detects acceleration, not sustained
// speed. Steady movement produces an excess proportional to velocity, so the
// separation between legitimate and cheating movement is narrower than it looks
// and very fast constant-velocity movement is caught while moderately fast
// constant-velocity movement is not.
//
// Second, elytra and riptide movement legitimately exceeds these bounds and is
// not distinguished. A production version needs to exclude those states.
type CheckSpeed struct{}

func (CheckSpeed) Name() string { return "CheckSpeed" }

func (CheckSpeed) Inspect(pkt *Packet, prof *Profile) bool {
	if pkt.Type != packetFlying {
		return false
	}

	// Derive a real time delta rather than assuming one packet is one tick.
	// The old code compared two positions with no delta at all, which baked a
	// fixed 50 ms into an undocumented constant and made the check wrong under
	// packet loss, lag, and deliberately withheld bursts.
	gap := float64(pkt.Timestamp - prof.lastPacketMs)
	prof.lastPacketMs = pkt.Timestamp

	// Gated on the SERVER's view, not the client's. Gating on the client's
	// claim meant a client that always reported itself grounded never entered
	// this branch, so the check was disabled by the single most common cheat
	// module in existence without the cheater doing anything CloudAC-specific.
	if !prof.serverOnGround && !prof.lastServerOnGround {
		prof.speed.AirborneTicks++
	} else {
		prof.speed.AirborneTicks = 0
		prof.speed.VL = 0
		prof.speed.LastOffsetH = 0
		return false
	}

	dx := prof.latestLocation.X - prof.lastLocation.X
	dz := prof.latestLocation.Z - prof.lastLocation.Z
	offsetH := math.Sqrt(dx*dx + dz*dz)

	// The Z term used to read the X coordinate. The resulting quantity was not
	// a displacement at all, it was the gap between the player's current X and
	// their previous Z, so a stationary player at x=100, z=-100 produced a
	// constant apparent movement of 200 blocks.

	settled := prof.speed.AirborneTicks >= speedSettleTicks
	usableGap := gap > 0 && gap <= maxPlausibleGapMillis

	defer func() { prof.speed.LastOffsetH = offsetH }()

	if !settled || !usableGap {
		prof.speed.VL = 0
		return false
	}

	// Friction compounds once per tick, so it is raised to the number of ticks
	// the gap actually covers.
	ticks := gap / tickMillis
	predicted := prof.speed.LastOffsetH * math.Pow(airFriction, ticks)
	excess := offsetH - predicted

	if excess <= speedExcessThreshold {
		prof.speed.VL = 0
		return false
	}

	prof.speed.VL++
	if prof.speed.VL < speedViolationsToFlag {
		return false
	}
	prof.speed.VL = 0
	return true
}
