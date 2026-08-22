package sys

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

const testUUID = "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"

// sim drives packets through the real dispatch path.
//
// It deliberately goes through dispatch rather than calling a check directly,
// because the processor-then-check ordering is itself load-bearing and a test
// that skipped it would not notice the ordering being reversed.
type sim struct {
	t     *testing.T
	s     *server
	now   time.Time
	flags []string
}

func newSim(t *testing.T) *sim {
	t.Helper()
	resetProfilesForTest()
	return &sim{
		t: t,
		s: &server{
			cfg: Config{
				TransmitterAddr: "127.0.0.1:0",
				MaxProfiles:     100,
				ProfileTTL:      time.Hour,
			},
			processors: []Processor{StatusProcessor{}},
			checks:     []Check{CheckAbilities{}, CheckSpeed{}},
		},
		now: time.UnixMilli(1_700_000_000_000),
	}
}

func (x *sim) feed(pkt *Packet) {
	x.t.Helper()
	pkt.UUID = testUUID
	pkt.Timestamp = x.now.UnixMilli()
	for _, v := range x.s.dispatch(pkt, x.now) {
		x.flags = append(x.flags, v.Check)
	}
	x.now = x.now.Add(50 * time.Millisecond)
}

func (x *sim) join() { x.feed(&Packet{Type: packetJoin}) }

// move sends one FLYING packet. serverGround is the authoritative ground state,
// clientGround is what the client claims, and they are separate arguments so a
// test can make them disagree.
func (x *sim) move(px, py, pz float64, serverGround, clientGround bool) {
	x.feed(&Packet{
		Type:           packetFlying,
		HasPosition:    true,
		HasLook:        true,
		X:              px,
		Y:              py,
		Z:              pz,
		OnGround:       clientGround,
		ServerOnGround: serverGround,
	})
}

// TestCheckSpeed pins the two defects that used to mask each other.
//
// Verified transition: with the write-back missing the cheat row fails, with
// the write-back fixed but the axis still wrong the stationary row fails, and
// only with both correct does the whole table pass.
func TestCheckSpeed(t *testing.T) {
	stationary := func(int, [3]float64) [3]float64 { return [3]float64{100, 70, -100} }

	constantSpeed := func(v float64) func(int, [3]float64) [3]float64 {
		return func(_ int, p [3]float64) [3]float64 {
			return [3]float64{p[0], p[1], p[2] + v}
		}
	}

	tests := []struct {
		name        string
		spawn       [3]float64
		step        func(int, [3]float64) [3]float64
		ticks       int
		wantFlagged bool
		why         string
	}{
		{
			name:  "stationary airborne at large non-zero coordinates",
			spawn: [3]float64{100, 70, -100},
			step:  stationary, ticks: 15, wantFlagged: false,
			why: "with the Z offset read off the X axis this player appears to " +
				"move 200 blocks per tick without having moved at all",
		},
		{
			name:  "stationary airborne at the origin",
			spawn: [3]float64{0, 70, 0},
			step:  stationary, ticks: 15, wantFlagged: false,
			why: "the origin is the one position where a wrong axis is invisible",
		},
		{
			name:  "walking",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(0.2158), ticks: 15, wantFlagged: false,
			why: "legitimate movement must never flag",
		},
		{
			name:  "sprinting",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(0.2806), ticks: 15, wantFlagged: false,
			why: "legitimate movement must never flag",
		},
		{
			name:  "sprint-jump arc",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(0.3300), ticks: 15, wantFlagged: false,
			why: "an airborne jump arc lasts about twelve ticks, four times the " +
				"consecutive violations a flag requires",
		},
		{
			name:  "sprinting with Speed II",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(0.3648), ticks: 15, wantFlagged: false,
			why: "legitimate movement must never flag",
		},
		{
			name:  "legacy sprint-jump peak",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(0.4000), ticks: 15, wantFlagged: false,
			why: "the fastest legitimate airborne case measured",
		},
		{
			name:  "genuine speed cheat, two blocks per tick",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(2.0), ticks: 15, wantFlagged: true,
			why: "this is the positive row. A check whose state is discarded " +
				"passes every negative row above and fails only here",
		},
		{
			name:  "genuine speed cheat, one block per tick",
			spawn: [3]float64{0, 70, 0},
			step:  constantSpeed(1.0), ticks: 15, wantFlagged: true,
			why: "twenty blocks per second, about seven times sprinting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newSim(t)
			x.join()

			pos := tt.spawn
			// One grounded packet, then the transition, so the airborne run
			// starts from a settled state.
			x.move(pos[0], pos[1], pos[2], true, true)
			for i := 0; i < tt.ticks; i++ {
				pos = tt.step(i, pos)
				x.move(pos[0], pos[1], pos[2], false, false)
			}

			got := 0
			for _, f := range x.flags {
				if f == "CheckSpeed" {
					got++
				}
			}
			// Legitimate movement must produce exactly zero. A sustained cheat
			// re-flags on every further run of consecutive violations, so the
			// positive rows assert at least one rather than an exact count.
			if tt.wantFlagged && got == 0 {
				t.Errorf("CheckSpeed did not flag\n  %s", tt.why)
			}
			if !tt.wantFlagged && got != 0 {
				t.Errorf("CheckSpeed produced %d false positives\n  %s", got, tt.why)
			}
		})
	}
}

// TestNoFallDoesNotDisableTheCheck is the regression test for the bypass.
//
// The client claims to be standing on the ground on every packet, which is what
// a NoFall module does, while the server reports it airborne. The check must
// follow the server.
func TestNoFallDoesNotDisableTheCheck(t *testing.T) {
	x := newSim(t)
	x.join()

	z := 0.0
	x.move(0, 70, z, true, true)
	for i := 0; i < 15; i++ {
		z += 2.0
		// serverGround false, clientGround true. The client is lying.
		x.move(0, 70, z, false, true)
	}

	if len(x.flags) == 0 {
		t.Fatal("a client claiming onGround while the server reports it airborne " +
			"and moving at 2 blocks per tick was not flagged: the check is " +
			"reading the client's claim again")
	}
}

// TestGroundedPlayerIsNeverChecked confirms the check stays off on the ground,
// where friction prediction does not apply.
func TestGroundedPlayerIsNeverChecked(t *testing.T) {
	x := newSim(t)
	x.join()

	z := 0.0
	for i := 0; i < 20; i++ {
		z += 2.0
		x.move(0, 70, z, true, true)
	}
	if len(x.flags) != 0 {
		t.Fatalf("grounded player produced %d flags", len(x.flags))
	}
}

// TestRejoinResetsProfile covers the stale-profile defect.
//
// A lost LEAVE used to leave a profile holding the player's logout coordinates,
// and the rejoin did not clear it because profile creation was conditional on
// absence. The first movement packet afterwards then measured displacement
// across the whole distance from logout position to spawn.
func TestRejoinResetsProfile(t *testing.T) {
	x := newSim(t)
	x.join()
	for i := 0; i < 4; i++ {
		x.move(1000, 70, 1000, false, false)
	}

	// LEAVE is dropped on the wire here. Only the rejoin arrives.
	x.join()

	profileMu.Lock()
	prof, ok := getProfile(testUUID)
	profileMu.Unlock()
	if !ok {
		t.Fatal("profile missing after rejoin")
	}
	if prof.latestLocation.X != 0 || prof.latestLocation.Z != 0 {
		t.Fatalf("rejoin left stale coordinates %v, want a zeroed profile", prof.latestLocation)
	}
	if prof.speed.VL != 0 || prof.speed.LastOffsetH != 0 {
		t.Fatalf("rejoin left stale speed state %+v", prof.speed)
	}

	before := len(x.flags)
	x.move(0, 70, 0, false, false)
	if len(x.flags) != before {
		t.Fatal("first packet after a rejoin flagged the player")
	}
}

// TestCheckAbilitiesFlagsOnceNotPerPacket covers the kick-storm shape.
func TestCheckAbilitiesFlagsOnceNotPerPacket(t *testing.T) {
	x := newSim(t)
	x.join()

	// The server never granted flight.
	x.feed(&Packet{Type: packetServerAbilities, IsFlying: false, CanFly: false})
	// The client claims it repeatedly.
	for i := 0; i < 10; i++ {
		x.feed(&Packet{Type: packetClientAbilities, IsFlying: true, CanFly: true})
	}

	got := 0
	for _, f := range x.flags {
		if f == "CheckAbilities" {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("CheckAbilities fired %d times for one continuous offence, want 1", got)
	}
}

// TestUnknownPlayerIsNotChecked confirms a packet for an untracked player never
// reaches a check.
//
// A missing key used to yield a zero-valued profile whose ground flags were
// both false, which put a phantom player straight into the airborne path.
func TestUnknownPlayerIsNotChecked(t *testing.T) {
	x := newSim(t)
	// No join.
	for i := 0; i < 10; i++ {
		x.move(0, 70, float64(i)*5, false, false)
	}
	if len(x.flags) != 0 {
		t.Fatalf("untracked player produced %d flags", len(x.flags))
	}
}

// TestProfileCapIsEnforced covers unbounded growth from injected joins.
func TestProfileCapIsEnforced(t *testing.T) {
	resetProfilesForTest()
	s := &server{
		cfg:        Config{MaxProfiles: 8, ProfileTTL: time.Hour},
		processors: []Processor{StatusProcessor{}},
	}
	now := time.UnixMilli(1_700_000_000_000)
	for i := 0; i < 50; i++ {
		s.dispatch(&Packet{
			Type: packetJoin,
			UUID: fmt.Sprintf("f81d4fae-7dec-11d0-a765-%012d", i),
		}, now)
	}
	profileMu.Lock()
	n := profileCount()
	profileMu.Unlock()
	if n > 8 {
		t.Fatalf("profile store grew to %d, cap is 8", n)
	}
}

// TestStaleProfilesAreEvicted covers a lost LEAVE leaking an entry forever.
func TestStaleProfilesAreEvicted(t *testing.T) {
	resetProfilesForTest()
	s := &server{
		cfg:        Config{MaxProfiles: 100, ProfileTTL: time.Minute},
		processors: []Processor{StatusProcessor{}},
	}
	start := time.UnixMilli(1_700_000_000_000)
	s.dispatch(&Packet{Type: packetJoin, UUID: testUUID}, start)

	// No LEAVE ever arrives. Some other player keeps the server busy later.
	other := "f81d4fae-7dec-11d0-a765-00a0c91e6bf7"
	s.dispatch(&Packet{Type: packetJoin, UUID: other}, start.Add(10*time.Minute))

	profileMu.Lock()
	_, stillThere := getProfile(testUUID)
	profileMu.Unlock()
	if stillThere {
		t.Fatal("a profile older than the TTL survived the sweep")
	}
}

// TestConcurrentDispatchIsRaceFree exercises the lock that makes
// goroutine-per-connection safe.
//
// This test could not have existed before. The profile store was an unguarded
// map, and Go answers a concurrent map write with an unrecoverable runtime
// abort that no test framework can contain, so adding concurrency without the
// lock did not fail a test, it killed the whole test binary.
//
// Run with -race.
func TestConcurrentDispatchIsRaceFree(t *testing.T) {
	resetProfilesForTest()
	s := &server{
		cfg:        Config{MaxProfiles: 500, ProfileTTL: time.Hour, TransmitterAddr: "127.0.0.1:0"},
		processors: []Processor{StatusProcessor{}},
		checks:     []Check{CheckAbilities{}, CheckSpeed{}},
	}
	now := time.UnixMilli(1_700_000_000_000)

	var wg sync.WaitGroup
	for p := 0; p < 8; p++ {
		uuid := fmt.Sprintf("f81d4fae-7dec-11d0-a765-%012d", p)
		s.dispatch(&Packet{Type: packetJoin, UUID: uuid}, now)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s.dispatch(&Packet{
					Type: packetFlying, UUID: uuid, HasPosition: true,
					Timestamp: now.UnixMilli() + int64(i*50),
					X:         float64(i), Y: 70, Z: float64(i),
				}, now)
			}(i)
		}
	}
	wg.Wait()
}
