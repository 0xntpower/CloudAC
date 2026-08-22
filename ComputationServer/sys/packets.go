package sys

// PacketType identifies what a message describes.
//
// The underlying type is int rather than int64, and the values come from iota
// rather than being written out, both of which are the ordinary Go shape for a
// small enumeration. The names are MixedCaps and unexported because nothing
// outside this package refers to them.
type PacketType int

const (
	packetJoin PacketType = iota
	packetLeave
	packetFlying
	packetClientAbilities
	packetServerAbilities
)

// String makes log lines and test failures readable. Without it every
// diagnostic printed a bare integer.
func (p PacketType) String() string {
	switch p {
	case packetJoin:
		return "JOIN"
	case packetLeave:
		return "LEAVE"
	case packetFlying:
		return "FLYING"
	case packetClientAbilities:
		return "CLIENT_ABILITIES"
	case packetServerAbilities:
		return "SERVER_ABILITIES"
	default:
		return "UNKNOWN"
	}
}
