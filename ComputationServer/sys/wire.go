package sys

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// wireVersion is the protocol generation. Both sides check it, so a mismatched
// deployment fails loudly at the first message instead of silently misparsing
// positional fields.
const wireVersion = "v1"

// worldLimit is Minecraft's world border. Coordinates beyond it are not
// plausible, and accepting them lets a peer poison a profile's position state.
const worldLimit = 3.0e7

var (
	errShortFrame   = errors.New("frame has fewer than three sections")
	errBadVersion   = errors.New("unsupported wire version")
	errUnauthorized = errors.New("message authentication failed")
	errStale        = errors.New("message timestamp outside the accepted window")

	uuidPattern = regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Packet is a fully parsed, fully validated message.
//
// Parsing happens exactly once, at the wire boundary, and every processor and
// check receives this struct. The previous design passed a []string down and
// re-split it inside both dispatch loops, so allocation and re-parsing grew
// linearly with the number of registered checks. This shape is constant in
// check count by construction.
type Packet struct {
	Type      PacketType
	UUID      string
	Timestamp int64

	// FLYING only.
	HasPosition bool
	HasLook     bool
	X, Y, Z     float64
	Yaw, Pitch  float32

	// OnGround is the client's own claim, taken straight off its packet.
	// Treat it as an assertion by a potentially hostile party, never as fact.
	OnGround bool

	// ServerOnGround is the game server's authoritative view.
	//
	// This field exists because every security decision here used to rest on
	// OnGround above. A client that always claims to be on the ground never
	// entered the movement check at all, which is exactly what a NoFall module
	// does and mainstream cheat clients ship one enabled by default.
	ServerOnGround bool

	// CLIENT_ABILITIES and SERVER_ABILITIES only.
	//
	// Note that for CLIENT_ABILITIES these are the client's own assertions.
	// See the warning on CheckAbilities.
	IsFlying bool
	CanFly   bool
}

// fieldCount is the exact number of pipe-separated fields each packet type
// carries. Validating arity once, here, is what stops a short message reaching
// an unchecked index further down. A bare TCP connect that sends nothing used
// to terminate the process through exactly that path.
var fieldCount = map[PacketType]int{
	packetJoin:            3,
	packetLeave:           3,
	packetFlying:          12,
	packetClientAbilities: 5,
	packetServerAbilities: 5,
}

// packetTypes maps the wire's type digit onto a PacketType.
//
// Lookups against it must use the comma-ok form. A plain map read returns the
// zero value on a miss, and the zero PacketType is packetJoin, so an unknown or
// empty type digit used to be silently reclassified as a player join.
var packetTypes = map[string]PacketType{
	"0": packetJoin,
	"1": packetLeave,
	"2": packetFlying,
	"3": packetClientAbilities,
	"4": packetServerAbilities,
}

// EncodeFrame wraps a payload in a versioned, authenticated frame.
//
// With no secret configured the MAC section is empty. That is only reachable
// when both endpoints are loopback, which Config.Validate enforces at startup.
func EncodeFrame(secret []byte, payload string) string {
	return wireVersion + "|" + macHex(secret, payload) + "|" + payload
}

// DecodeFrame verifies a frame and returns its payload.
func DecodeFrame(secret []byte, line string) (string, error) {
	// TrimRight rather than Trim with a "\n" cutset. A Windows-hosted game
	// server sends CRLF, and stripping only the newline left a carriage return
	// welded to the final field, where it silently broke every float parse.
	line = strings.TrimRight(line, "\r\n")

	version, rest, ok := strings.Cut(line, "|")
	if !ok {
		return "", errShortFrame
	}
	if version != wireVersion {
		return "", fmt.Errorf("%w: %q", errBadVersion, version)
	}
	mac, payload, ok := strings.Cut(rest, "|")
	if !ok {
		return "", errShortFrame
	}
	if !hmac.Equal([]byte(mac), []byte(macHex(secret, payload))) {
		return "", errUnauthorized
	}
	return payload, nil
}

func macHex(secret []byte, payload string) string {
	if len(secret) == 0 {
		return ""
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

// ParsePacket validates a payload completely before any consumer sees it.
//
// Everything downstream may assume: the type is known, the field count is
// exact, the UUID is well formed, every number parsed, and every coordinate is
// finite and inside the world border.
func ParsePacket(payload string, maxSkew time.Duration, now time.Time) (*Packet, error) {
	args := strings.Split(payload, "|")
	if len(args) < 3 {
		return nil, fmt.Errorf("payload has %d fields, need at least 3", len(args))
	}

	typ, known := packetTypes[args[0]]
	if !known {
		return nil, fmt.Errorf("unknown packet type %q", args[0])
	}
	if want := fieldCount[typ]; len(args) != want {
		return nil, fmt.Errorf("packet type %s wants %d fields, got %d", typ, want, len(args))
	}
	if !uuidPattern.MatchString(args[1]) {
		return nil, fmt.Errorf("malformed uuid %q", args[1])
	}

	ts, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("timestamp: %w", err)
	}
	// Freshness is what stops a captured line being replayed forever. It is
	// only meaningful alongside the MAC, so it is skipped when unauthenticated.
	if maxSkew > 0 {
		if skew := now.Sub(time.UnixMilli(ts)); skew > maxSkew || skew < -maxSkew {
			return nil, fmt.Errorf("%w: %s", errStale, skew)
		}
	}

	p := &Packet{Type: typ, UUID: args[1], Timestamp: ts}

	switch typ {
	case packetJoin, packetLeave:
		// No further fields.

	case packetFlying:
		if p.HasPosition, err = strconv.ParseBool(args[3]); err != nil {
			return nil, fmt.Errorf("hasPosition: %w", err)
		}
		if p.HasLook, err = strconv.ParseBool(args[4]); err != nil {
			return nil, fmt.Errorf("hasLook: %w", err)
		}
		if p.OnGround, err = strconv.ParseBool(args[5]); err != nil {
			return nil, fmt.Errorf("onGround: %w", err)
		}
		if p.X, err = parseCoord(args[6], "x"); err != nil {
			return nil, err
		}
		if p.Y, err = parseCoord(args[7], "y"); err != nil {
			return nil, err
		}
		if p.Z, err = parseCoord(args[8], "z"); err != nil {
			return nil, err
		}
		yaw, err := parseAngle(args[9], "yaw")
		if err != nil {
			return nil, err
		}
		pitch, err := parseAngle(args[10], "pitch")
		if err != nil {
			return nil, err
		}
		p.Yaw, p.Pitch = yaw, pitch

		if p.ServerOnGround, err = strconv.ParseBool(args[11]); err != nil {
			return nil, fmt.Errorf("serverOnGround: %w", err)
		}

	case packetClientAbilities, packetServerAbilities:
		if p.IsFlying, err = strconv.ParseBool(args[3]); err != nil {
			return nil, fmt.Errorf("isFlying: %w", err)
		}
		if p.CanFly, err = strconv.ParseBool(args[4]); err != nil {
			return nil, fmt.Errorf("canFly: %w", err)
		}
	}

	return p, nil
}

// parseCoord rejects NaN and the infinities.
//
// Go's ParseFloat accepts "NaN", "Infinity" and "-Infinity" with a nil error,
// and Java's Double.toString emits exactly those spellings. A modified client
// can therefore put a NaN into a position packet, and every subsequent
// comparison against NaN is false, which makes the player permanently
// unflaggable.
func parseCoord(s, name string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s is not finite: %q", name, s)
	}
	if math.Abs(v) > worldLimit {
		return 0, fmt.Errorf("%s outside the world border: %v", name, v)
	}
	return v, nil
}

func parseAngle(s, name string) (float32, error) {
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s is not finite: %q", name, s)
	}
	return float32(v), nil
}
