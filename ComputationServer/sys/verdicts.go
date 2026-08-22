package sys

import (
	"fmt"
	"log/slog"
	"net"
	"time"
)

// verdictDialTimeout bounds the connect. Without a timeout a dropped SYN parks
// the delivery for the operating system default, which on stock Linux is over
// two minutes.
const verdictDialTimeout = 3 * time.Second

// deliverVerdict sends one verdict to the transmitter.
//
// It is a package-level variable so tests can substitute it. That single change
// is what makes both checks unit-testable without a network.
var deliverVerdict = func(cfg Config, v Verdict, now time.Time) error {
	conn, err := net.DialTimeout("tcp", cfg.TransmitterAddr, verdictDialTimeout)
	if err != nil {
		// Returning rather than dereferencing. The dial error used to be
		// discarded, which left a nil connection that the following write
		// dereferenced, so a routine game-server restart killed this daemon on
		// its very next detection.
		return fmt.Errorf("dial transmitter: %w", err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(now.Add(verdictDialTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}

	payload := fmt.Sprintf("FLAG|%s|%s|%d", v.UUID, v.Check, now.UnixMilli())
	frame := EncodeFrame(cfg.Secret, payload)

	// Fprintln, not Fprintf. Passing a built string as the format argument made
	// any percent sign in it a format verb, and the UUID reaches this line from
	// the network, so a crafted value corrupted the verdict.
	if _, err := fmt.Fprintln(conn, frame); err != nil {
		return fmt.Errorf("write verdict: %w", err)
	}
	return nil
}

func (s *server) sendVerdict(v Verdict) {
	now := time.Now()
	if err := deliverVerdict(s.cfg, v, now); err != nil {
		s.verdictsFailed.Add(1)
		// A verdict that cannot be delivered is a detection that silently did
		// nothing, so it is logged at error level and counted.
		slog.Error("verdict delivery failed", "uuid", v.UUID, "check", v.Check, "err", err)
		return
	}
	s.flagsDelivered.Add(1)
	slog.Warn("player flagged", "uuid", v.UUID, "check", v.Check)
}
