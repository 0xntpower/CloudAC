package sys

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.UnixMilli(1_700_000_000_000)

func flyingPayload() string {
	return "2|" + testUUID + "|1700000000000|true|true|false|123.5|64.0|-77.25|90.0|0.0|false"
}

// TestParsePacketRejectsHostileInput is the corpus of everything that used to
// crash, corrupt, or silently misclassify.
//
// The single assertion that matters for a daemon on an open port is that
// arbitrary bytes must not terminate the process. A bare TCP connect that
// closed without sending anything used to do exactly that, which meant an nmap
// sweep or a container liveness probe was enough to take it down.
func TestParsePacketRejectsHostileInput(t *testing.T) {
	cases := []struct {
		name, payload, defect string
	}{
		{"empty", "", "a TCP connect that closes without sending. A port scan was enough"},
		{"one field", "2", "an unchecked index reached past the end of the slice"},
		{"two fields", "2|" + testUUID, "same"},
		{"type only, trailing pipe", "2|", "same"},
		{"flying short by one", "2|" + testUUID + "|1700000000000|true|true|false|1|2|3|0|0", "arity was never checked"},
		{"flying long by one", flyingPayload() + "|extra", "arity was never checked"},
		{"unknown packet type", "9|" + testUUID + "|1700000000000", "a map miss returned the zero value, which is JOIN"},
		{"empty packet type", "|" + testUUID + "|1700000000000", "silently became JOIN"},
		{"malformed uuid", "0|not-a-uuid|1700000000000", "reached UUID.fromString on the other side and threw past a close"},
		{"uuid with a pipe", "0|aaaa|bbbb|1700000000000", "no delimiter escaping"},
		{"uuid with a format verb", "0|%s%d-7dec-11d0-a765-00a0c91e6bf6|1700000000000", "reached a format string"},
		{"nan coordinates", "2|" + testUUID + "|1700000000000|true|true|false|NaN|64|NaN|0|0|false", "NaN parsed with a nil error and made the player unflaggable"},
		{"infinity coordinates", "2|" + testUUID + "|1700000000000|true|true|false|Inf|64|Inf|0|0|false", "same"},
		{"long-form infinity", "2|" + testUUID + "|1700000000000|true|true|false|Infinity|64|-Infinity|0|0|false", "Java writes this exact spelling"},
		{"coordinates past the world border", "2|" + testUUID + "|1700000000000|true|true|false|1e30|64|1e30|0|0|false", "position state could be poisoned"},
		{"garbage booleans", "2|" + testUUID + "|1700000000000|yes|no|maybe|1|2|3|0|0|false", "parse errors were discarded and became false"},
		{"garbage floats", "2|" + testUUID + "|1700000000000|true|true|false|x|y|z|q|r|false", "parse errors were discarded and became zero"},
		{"non-numeric timestamp", "0|" + testUUID + "|not-a-number", "never parsed at all"},
		{"oversized field", "0|" + strings.Repeat("A", 1<<16) + "|1700000000000", "no length cap anywhere"},
		{"nul byte", "0|" + testUUID + "\x00|1700000000000", "passed straight through"},
		{"ansi escape", "0|\x1b[2J\x1b[1;31m" + testUUID + "|1700000000000", "reached the operator console unfiltered"},
		{"bidi override", "0|‮gnitaehc" + testUUID + "|1700000000000", "visually reversed a log line"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC: %v\n  defect: %s", r, tc.defect)
				}
			}()
			if _, err := ParsePacket(tc.payload, 0, testNow); err == nil {
				t.Fatalf("accepted hostile input %q\n  defect: %s", tc.payload, tc.defect)
			}
		})
	}
}

// TestParsePacketAcceptsWellFormedInput is the counterweight. A validator that
// rejects everything would pass the table above.
func TestParsePacketAcceptsWellFormedInput(t *testing.T) {
	good := []string{
		"0|" + testUUID + "|1700000000000",
		"1|" + testUUID + "|1700000000000",
		flyingPayload(),
		"3|" + testUUID + "|1700000000000|true|false",
		"4|" + testUUID + "|1700000000000|false|true",
	}
	for _, payload := range good {
		if _, err := ParsePacket(payload, 0, testNow); err != nil {
			t.Errorf("rejected valid payload %q: %v", payload, err)
		}
	}
}

// TestDecodeFrameHandlesCRLF covers the platform-dependent corruption.
//
// A Windows-hosted game server writes CRLF. Stripping only the newline left a
// carriage return welded to the final field, where it broke the float parse
// silently because the error was discarded.
func TestDecodeFrameHandlesCRLF(t *testing.T) {
	secret := []byte("a-shared-secret-of-adequate-length")
	frame := EncodeFrame(secret, flyingPayload())

	for _, ending := range []string{"", "\n", "\r\n"} {
		payload, err := DecodeFrame(secret, frame+ending)
		if err != nil {
			t.Fatalf("ending %q: %v", ending, err)
		}
		pkt, err := ParsePacket(payload, 0, testNow)
		if err != nil {
			t.Fatalf("ending %q: %v", ending, err)
		}
		if pkt.Pitch != 0 {
			t.Fatalf("ending %q: pitch corrupted to %v", ending, pkt.Pitch)
		}
	}
}

// TestDecodeFrameRejectsForgeryAndReplay covers the unauthenticated channels.
func TestDecodeFrameRejectsForgeryAndReplay(t *testing.T) {
	secret := []byte("a-shared-secret-of-adequate-length")
	payload := "0|" + testUUID + "|1700000000000"

	t.Run("unsigned frame is rejected", func(t *testing.T) {
		if _, err := DecodeFrame(secret, "v1||"+payload); err == nil {
			t.Fatal("a frame with no MAC was accepted")
		}
	})

	t.Run("forged mac is rejected", func(t *testing.T) {
		if _, err := DecodeFrame(secret, "v1|deadbeef|"+payload); err == nil {
			t.Fatal("a frame with a forged MAC was accepted")
		}
	})

	t.Run("wrong secret is rejected", func(t *testing.T) {
		frame := EncodeFrame([]byte("some-other-secret-entirely-ok"), payload)
		if _, err := DecodeFrame(secret, frame); err == nil {
			t.Fatal("a frame signed with the wrong secret was accepted")
		}
	})

	t.Run("wrong version is rejected", func(t *testing.T) {
		if _, err := DecodeFrame(secret, "v2|"+"x"+"|"+payload); err == nil {
			t.Fatal("a frame with an unknown version was accepted")
		}
	})

	t.Run("stale timestamp is rejected", func(t *testing.T) {
		stale := "0|" + testUUID + "|1600000000000"
		if _, err := ParsePacket(stale, 30*time.Second, testNow); err == nil {
			t.Fatal("a replayed message from years ago was accepted")
		}
	})
}

// TestConfigRefusesUnauthenticatedPublicBind covers the configuration that
// exposes an unauthenticated port, which is the highest-severity way this
// program can be started.
func TestConfigRefusesUnauthenticatedPublicBind(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"loopback with no secret is allowed", Config{ListenAddr: "127.0.0.1:1234", TransmitterAddr: "127.0.0.1:1212"}, false},
		{"all interfaces with no secret is refused", Config{ListenAddr: ":1234", TransmitterAddr: "127.0.0.1:1212"}, true},
		{"public bind with no secret is refused", Config{ListenAddr: "0.0.0.0:1234", TransmitterAddr: "127.0.0.1:1212"}, true},
		{"remote transmitter with no secret is refused", Config{ListenAddr: "127.0.0.1:1234", TransmitterAddr: "10.0.0.5:1212"}, true},
		{"public bind with a secret is allowed", Config{ListenAddr: "0.0.0.0:1234", TransmitterAddr: "10.0.0.5:1212", Secret: []byte("a-shared-secret-of-adequate-length")}, false},
		{"a short secret is refused", Config{ListenAddr: "0.0.0.0:1234", TransmitterAddr: "10.0.0.5:1212", Secret: []byte("short")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("configuration was accepted but should have been refused")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("configuration was refused: %v", err)
			}
		})
	}
}

// FuzzParsePacket is the standing guarantee.
//
// The corpus above is the set of inputs a human thought of. This covers the
// rest. Against the previous implementation it crashed before it finished
// gathering baseline coverage of its own seed corpus.
func FuzzParsePacket(f *testing.F) {
	for _, s := range []string{
		"", "\n", "|", "2", "2|", "9|x|0",
		"0|" + testUUID + "|1700000000000",
		flyingPayload(),
		"2|" + testUUID + "|1700000000000|true|true|false|NaN|0|NaN|0|0|false",
		"3|" + testUUID + "|1700000000000|true|true",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		// The contract is simply that this never panics.
		_, _ = ParsePacket(payload, 0, testNow)
	})
}

// FuzzDecodeFrame fuzzes the outer framing, including the MAC path.
func FuzzDecodeFrame(f *testing.F) {
	secret := []byte("a-shared-secret-of-adequate-length")
	f.Add("")
	f.Add("v1||0|" + testUUID + "|1700000000000")
	f.Add(EncodeFrame(secret, flyingPayload()))
	f.Fuzz(func(t *testing.T, line string) {
		if payload, err := DecodeFrame(secret, line); err == nil {
			_, _ = ParsePacket(payload, 0, testNow)
		}
	})
}
