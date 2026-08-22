package sys

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// heartbeatInterval is how often liveness is reported.
//
// The counters below are incremented on the work path, after a message has been
// fully dispatched. That placement is the whole point. A timer that merely
// proves a goroutine is scheduled reports healthy during exactly the failure it
// exists to catch, because a blocked accept loop does not stop the Go scheduler
// from running everything else. Liveness has to be derived from progress.
const heartbeatInterval = 30 * time.Second

type server struct {
	cfg     Config
	version string

	checks     []Check
	processors []Processor

	packetsProcessed atomic.Uint64
	packetsRejected  atomic.Uint64
	flagsDelivered   atomic.Uint64
	verdictsFailed   atomic.Uint64
	connections      atomic.Int64
}

// Start runs the computation server until interrupted.
func Start(version string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	s := &server{
		cfg:        cfg,
		version:    version,
		processors: []Processor{StatusProcessor{}},
		// A slice literal rather than make-with-a-length followed by indexed
		// assignment. The previous form carried the count in two places, so
		// adding a check without also editing the length panicked at startup.
		checks: []Check{CheckAbilities{}, CheckSpeed{}},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return s.run(ctx)
}

func (s *server) run(ctx context.Context) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		// Returned, not discarded. A discarded listen error left a nil listener
		// that the accept call dereferenced, and because the success banner was
		// printed first, a supervisor restart loop announced a successful start
		// on every iteration.
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}

	slog.Info("computation server started",
		"version", s.version,
		"listen", listener.Addr().String(),
		"transmitter", s.cfg.TransmitterAddr,
		"authenticated", s.cfg.Authenticated(),
	)
	if !s.cfg.Authenticated() {
		slog.Warn("running without a shared secret: loopback only, no peer authentication")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.heartbeat(ctx)
	}()

	// Closing the listener is what unblocks Accept on shutdown.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			slog.Error("accept failed", "err", err)
			// One failed accept must not end the loop.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, conn)
		}()
	}

	wg.Wait()
	slog.Info("computation server stopped",
		"packets", s.packetsProcessed.Load(),
		"rejected", s.packetsRejected.Load(),
	)
	return nil
}

// handleConn drains one connection until it closes.
//
// A goroutine per connection, and every message on that connection read in a
// loop. Reading exactly one message per accepted connection was an unwritten
// contract that neither side enforced, and it meant a peer switching to a
// persistent connection would have every message after the first silently
// dropped and then block once the socket buffer filled.
func (s *server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	s.connections.Add(1)
	defer s.connections.Add(-1)

	// A panic in one message must cost that connection, never the process.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("handler panic, dropping connection", "peer", conn.RemoteAddr().String(), "panic", r)
		}
	}()

	// LimitReader caps a single message, so a peer that never sends a newline
	// cannot grow the read buffer without bound.
	reader := bufio.NewReader(io.LimitReader(conn, s.cfg.MaxLineBytes*1024))

	for ctx.Err() == nil {
		// A read deadline is what stops one silent client parking this
		// connection indefinitely. Without it the process stayed alive with its
		// port open, detecting nothing, which is worse than crashing because
		// nothing observes it.
		if err := conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout)); err != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if len(line) > 0 {
				s.handleLine(line)
			}
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				slog.Debug("read ended", "peer", conn.RemoteAddr().String(), "err", err)
			}
			return
		}
		s.handleLine(line)
	}
}

func (s *server) handleLine(line string) {
	now := time.Now()

	payload, err := DecodeFrame(s.cfg.Secret, line)
	if err != nil {
		s.packetsRejected.Add(1)
		slog.Debug("frame rejected", "err", err)
		return
	}

	skew := s.cfg.MaxClockSkew
	if !s.cfg.Authenticated() {
		// Freshness only means something alongside a MAC. Enforcing it without
		// one would reject honest traffic from a clock-skewed host for no
		// security benefit.
		skew = 0
	}

	pkt, err := ParsePacket(payload, skew, now)
	if err != nil {
		s.packetsRejected.Add(1)
		slog.Debug("packet rejected", "err", err)
		return
	}

	verdicts := s.dispatch(pkt, now)
	s.packetsProcessed.Add(1)

	// Delivery happens outside the lock. A slow or unreachable transmitter must
	// not hold up processing for every other player.
	for _, v := range verdicts {
		s.sendVerdict(v)
	}
}

// dispatch runs processors then checks for one packet, under one lock.
//
// The lock spans both phases deliberately. Processors write the state checks
// read, so the sequence is a read-modify-write. Locking each map access
// individually would be cheaper and would still let a concurrent message for
// the same player land between the two phases, which would make a movement
// check compare positions from different packets.
func (s *server) dispatch(pkt *Packet, now time.Time) []Verdict {
	profileMu.Lock()
	defer profileMu.Unlock()

	if evicted := sweepProfiles(now, s.cfg.ProfileTTL); evicted > 0 {
		slog.Info("evicted stale profiles", "count", evicted, "tracked", profileCount())
	}

	for _, p := range s.processors {
		p.Process(pkt, now, s.cfg.MaxProfiles)
	}

	prof, ok := getProfile(pkt.UUID)
	if !ok {
		// Unknown player. Checks never run against a phantom zero-valued
		// profile, whose false ground flags used to put it straight into the
		// airborne detection path.
		return nil
	}

	var verdicts []Verdict
	for _, c := range s.checks {
		if c.Inspect(pkt, prof) {
			verdicts = append(verdicts, Verdict{UUID: pkt.UUID, Check: c.Name()})
		}
	}
	return verdicts
}

// heartbeat reports progress so a stalled pipeline is visible.
//
// Before this existed, the only output indicating the system worked end to end
// was a flag line, and flags are by design rare. A silent console was the
// expected output of a healthy anti-cheat and of a completely dead one, which
// meant four separate failure modes could stop detection indefinitely with
// nothing to observe.
func (s *server) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	var lastProcessed uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed := s.packetsProcessed.Load()
			delta := processed - lastProcessed
			lastProcessed = processed

			profileMu.Lock()
			tracked := profileCount()
			profileMu.Unlock()

			// The actionable signal is delta, not the total. A total that stops
			// advancing while players are online is the alert condition.
			slog.Info("heartbeat",
				"packets_total", processed,
				"packets_since_last", delta,
				"rejected_total", s.packetsRejected.Load(),
				"profiles_tracked", tracked,
				"flags_total", s.flagsDelivered.Load(),
				"verdicts_failed", s.verdictsFailed.Load(),
				"open_connections", s.connections.Load(),
			)
			if delta == 0 && tracked > 0 {
				slog.Error("no packets processed since the last heartbeat while players are tracked: detection may have stopped")
			}
		}
	}
}
