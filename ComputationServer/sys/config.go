package sys

import (
	"fmt"
	"net"
	"os"
	"time"
)

// Config holds everything that used to be a compile-time constant.
//
// Before this existed, the addresses were literals in three different files,
// which meant the README's central claim -- that the ComputationServer can run
// on a different machine -- was not reachable by any amount of configuration.
type Config struct {
	// ListenAddr is where packet data is accepted from the transmitter.
	ListenAddr string

	// TransmitterAddr is where verdicts are delivered back.
	TransmitterAddr string

	// Secret authenticates both directions. Empty means unauthenticated, which
	// is only permitted when both addresses are loopback. See Validate.
	Secret []byte

	// MaxClockSkew bounds how stale a message may be before it is rejected.
	// This is what stops a captured line being replayed indefinitely.
	MaxClockSkew time.Duration

	// ReadTimeout bounds how long a connection may sit idle mid-message.
	// Without it, one silent client blocks the accept loop forever.
	ReadTimeout time.Duration

	// MaxLineBytes caps a single message. Without it, a peer that never sends a
	// newline grows the read buffer without bound.
	MaxLineBytes int64

	// MaxProfiles caps the profile store so an authenticated-but-buggy or
	// hostile peer cannot grow it without limit.
	MaxProfiles int

	// ProfileTTL evicts profiles whose player left without a LEAVE arriving.
	ProfileTTL time.Duration
}

// LoadConfig reads configuration from the environment.
//
// Environment variables rather than a config file: the daemon has two values
// worth externalising, and adding a YAML parser for two strings would add the
// only third-party dependency in the component. That dependency-free property
// is what makes govulncheck's coverage of this binary complete.
func LoadConfig() (Config, error) {
	c := Config{
		ListenAddr:      envOr("CLOUDAC_LISTEN_ADDR", "127.0.0.1:1234"),
		TransmitterAddr: envOr("CLOUDAC_TRANSMITTER_ADDR", "127.0.0.1:1212"),
		Secret:          []byte(os.Getenv("CLOUDAC_SHARED_SECRET")),
		MaxClockSkew:    30 * time.Second,
		ReadTimeout:     30 * time.Second,
		MaxLineBytes:    8192,
		MaxProfiles:     5000,
		ProfileTTL:      30 * time.Minute,
	}
	return c, c.Validate()
}

// Validate refuses configurations that would expose an unauthenticated port.
//
// This is the control that makes the default safe. Binding a public interface
// with no shared secret is the single highest-severity configuration this
// program can be given, so it is refused at startup rather than warned about.
func (c Config) Validate() error {
	if len(c.Secret) > 0 {
		if len(c.Secret) < 16 {
			return fmt.Errorf("CLOUDAC_SHARED_SECRET must be at least 16 bytes, got %d", len(c.Secret))
		}
		return nil
	}
	if !isLoopback(c.ListenAddr) {
		return fmt.Errorf(
			"refusing to listen on %s without CLOUDAC_SHARED_SECRET: "+
				"an unauthenticated listener lets any peer forge packet data and kick players. "+
				"Either bind loopback (CLOUDAC_LISTEN_ADDR=127.0.0.1:1234) or set a shared secret",
			c.ListenAddr)
	}
	if !isLoopback(c.TransmitterAddr) {
		return fmt.Errorf(
			"refusing to send verdicts to %s without CLOUDAC_SHARED_SECRET: "+
				"verdicts would cross the network unauthenticated and in cleartext",
			c.TransmitterAddr)
	}
	return nil
}

// Authenticated reports whether messages are HMAC-protected.
func (c Config) Authenticated() bool { return len(c.Secret) > 0 }

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "localhost" {
		// An empty host means "all interfaces", which is the opposite of loopback.
		return host == "localhost"
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
