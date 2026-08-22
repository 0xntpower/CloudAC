package sys

import (
	"sync"
	"time"
)

// Location is one position sample.
type Location struct {
	X, Y, Z    float64
	Yaw, Pitch float32
}

// SpeedCheckData is CheckSpeed's per-player state.
type SpeedCheckData struct {
	// VL is the consecutive-violation counter. It resets on any non-violating
	// packet, so it measures an unbroken run rather than a lifetime total.
	VL int

	// LastOffsetH is the previous tick's horizontal distance, in blocks.
	//
	// This holds a LINEAR distance. It previously held a squared one while
	// being multiplied by a linear drag coefficient, which made the comparison
	// dimensionally meaningless.
	LastOffsetH float64

	// AirborneTicks counts consecutive airborne packets. The check needs a
	// settled airborne-to-airborne sample before its prediction means anything,
	// so the first couple of ticks after leaving the ground are skipped.
	AirborneTicks int
}

// Profile is everything tracked for one player.
type Profile struct {
	uuid string

	isFlyingClient        bool
	isFlyingServer        bool
	isAllowedFlightClient bool
	isAllowedFlightServer bool

	// onGround and lastOnGround are the CLIENT's claims. They are retained
	// only so a future check can compare them against the server's view, which
	// is a stronger signal than either value alone. No security decision may
	// rest on them.
	onGround     bool
	lastOnGround bool

	// serverOnGround and lastServerOnGround are the game server's
	// authoritative view, and are what the movement check gates on.
	serverOnGround     bool
	lastServerOnGround bool

	latestLocation Location
	lastLocation   Location

	// lastPacketMs is the previous packet's client timestamp, used to derive a
	// real time delta instead of assuming a fixed 50 ms tick.
	lastPacketMs int64

	// abilitiesFlagged latches CheckAbilities so a persistent bad state
	// produces one verdict rather than one per packet.
	abilitiesFlagged bool

	speed SpeedCheckData

	lastSeen time.Time
}

// profileMu guards profiles.
//
// dispatch holds this lock for the entire handling of one message, processors
// and checks together. Locking each access individually would be cheaper and
// wrong: a processor writes state that a check then reads, so the pair is a
// read-modify-write. A second goroutine landing between them would make a
// check compare a position from one packet against a position from the next.
var (
	profileMu sync.Mutex
	profiles  = make(map[string]*Profile)
	lastSweep time.Time
)

// addProfile creates or resets a player's profile. Callers must hold profileMu.
//
// This is unconditional on purpose. It used to be guarded by "only if absent",
// which meant a lost LEAVE left a stale profile holding the player's logout
// coordinates, and rejoining did not clear it. The first movement packet after
// the rejoin then measured displacement across the whole distance from logout
// position to spawn.
func addProfile(uuid string, now time.Time, max int) *Profile {
	if _, exists := profiles[uuid]; !exists && len(profiles) >= max {
		// Shed rather than grow without bound. Reaching this means either a
		// misconfigured cap or a peer inventing players.
		return nil
	}
	p := &Profile{
		uuid: uuid,
		// A freshly joined player is standing, not falling.
		onGround:           true,
		lastOnGround:       true,
		serverOnGround:     true,
		lastServerOnGround: true,
		lastSeen:           now,
	}
	profiles[uuid] = p
	return p
}

// removeProfile deletes a player's profile. Callers must hold profileMu.
func removeProfile(uuid string) {
	delete(profiles, uuid)
}

// getProfile returns a pointer to the stored profile, so mutations by a check
// land in the store rather than in a copy. Callers must hold profileMu.
//
// The map holds pointers for exactly this reason. When it held values, Get
// returned a copy, the processors wrote theirs back by hand and the checks did
// not, so CheckSpeed's violation counter was discarded on every single packet
// and its flag branch was unreachable for the entire life of the project.
//
// The comma-ok return is the other half: a plain map read on a missing key used
// to yield a zero-valued Profile, whose onGround and lastOnGround are both
// false, which put an unknown player straight into the airborne detection path.
func getProfile(uuid string) (*Profile, bool) {
	p, ok := profiles[uuid]
	return p, ok
}

// profileCount reports how many players are tracked. Callers must hold profileMu.
//
// Compared against the game server's online count, this is the cheapest
// available detector for the case where the daemon restarted and every online
// player silently stopped being tracked.
func profileCount() int { return len(profiles) }

// sweepProfiles evicts profiles whose player stopped being heard from.
// Callers must hold profileMu.
//
// A LEAVE can be lost to a dropped connection or a game-server crash, and
// without eviction those entries accumulate for the life of the process.
func sweepProfiles(now time.Time, ttl time.Duration) int {
	if now.Sub(lastSweep) < time.Minute {
		return 0
	}
	lastSweep = now
	cutoff := now.Add(-ttl)
	evicted := 0
	for uuid, p := range profiles {
		if p.lastSeen.Before(cutoff) {
			delete(profiles, uuid)
			evicted++
		}
	}
	return evicted
}

// resetProfilesForTest clears all state. Test-only helper.
func resetProfilesForTest() {
	profileMu.Lock()
	defer profileMu.Unlock()
	profiles = make(map[string]*Profile)
	lastSweep = time.Time{}
}
