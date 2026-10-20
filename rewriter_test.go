package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/williamfhe/godivert"
)

func TestRewriteUserAgentOnAnyTCPPort(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: original-agent/0.1\r\n\r\n")
	pkt := makeIPv4TCPPacket(43210, payload)
	cfg := &Config{TTL: 64, UA: "mobile"}

	_, uaChanged := rewrite(pkt, cfg)

	if !uaChanged {
		t.Fatal("expected User-Agent rewrite on a non-standard TCP port")
	}
	if !bytes.Contains(pkt.Raw, []byte("User-Agent: mobile")) {
		t.Fatalf("rewritten packet does not contain target User-Agent: %q", pkt.Raw)
	}
}

func makeIPv4TCPPacket(dstPort uint16, payload []byte) *godivert.Packet {
	raw := make([]byte, 40+len(payload))
	raw[0] = 0x45
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	raw[8] = 64
	raw[9] = 6
	binary.BigEndian.PutUint16(raw[20:22], 50000)
	binary.BigEndian.PutUint16(raw[22:24], dstPort)
	raw[32] = 0x50
	raw[33] = 0x18
	copy(raw[40:], payload)
	return &godivert.Packet{Raw: raw, PacketLen: uint(len(raw))}
}

func TestReplaceUserAgentReplacesExistingHeader(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: old\r\n\r\n")
	got := replaceUserAgent(payload, "new-agent")
	want := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: new\r\n\r\n")

	if !bytes.Equal(got, want) {
		t.Fatalf("unexpected payload:\nwant=%q\ngot=%q", want, got)
	}
}

func TestReplaceUserAgentPadsShorterValueToPreservePayloadLength(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: original-agent/0.1\r\n\r\n")
	got := replaceUserAgent(payload, "short")

	if len(got) != len(payload) {
		t.Fatalf("payload length changed: want=%d got=%d", len(payload), len(got))
	}
	if !bytes.Contains(got, []byte("User-Agent: short             \r\n")) {
		t.Fatalf("short UA was not padded in-place: %q", got)
	}
}

func TestReplaceUserAgentTruncatesLongerValueToPreservePayloadLength(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: original-agent/0.1\r\n\r\n")
	got := replaceUserAgent(payload, "ua3f-agent-0000001-extra")

	if len(got) != len(payload) {
		t.Fatalf("payload length changed: want=%d got=%d", len(payload), len(got))
	}
	if !bytes.Contains(got, []byte("User-Agent: ua3f-agent-0000001\r\n")) {
		t.Fatalf("long UA was not truncated in-place: %q", got)
	}
}

func TestReplaceUserAgentDoesNotInsertMissingHeader(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nAccept: */*\r\n\r\n")
	got := replaceUserAgent(payload, "new-agent")

	if got != nil {
		t.Fatalf("missing User-Agent should not be inserted in stable mode: %q", got)
	}
}

func TestReplaceUserAgentHeaderBoundaries(t *testing.T) {
	cases := []struct{ name, request, want string }{
		{"body only", "POST / HTTP/1.1\r\nHost: example.com\r\n\r\nUser-Agent: body-secret", ""},
		{"similar field", "GET / HTTP/1.1\r\nX-User-Agent: unchanged\r\n\r\n", ""},
		{"embedded header value", "GET / HTTP/1.1\r\nX-Test: User-Agent: unchanged\r\n\r\n", ""},
		{"incomplete headers", "GET / HTTP/1.1\r\nUser-Agent: original\r\n", ""},
		{"incomplete UA", "GET / HTTP/1.1\r\nUser-Agent: orig", ""},
		{"response", "HTTP/1.1 200 OK\r\nUser-Agent: original\r\n\r\n", ""},
		{"invalid request line", "GET / NOTHTTP\r\nUser-Agent: original\r\n\r\n", ""},
		{"folded line", "GET / HTTP/1.1\r\nUser-Agent: original\r\n continuation\r\n\r\n", ""},
		{"duplicate UA", "GET / HTTP/1.1\r\nUser-Agent: one\r\nUser-Agent: two\r\n\r\n", ""},
		{"bare newline", "GET / HTTP/1.1\r\nUser-Agent: orig\ninjected\r\n\r\n", ""},
		{"no space", "GET / HTTP/1.1\r\nUser-Agent:original\r\n\r\n", "GET / HTTP/1.1\r\nUser-Agent:mobile  \r\n\r\n"},
		{"mixed case and tabs", "GET / HTTP/1.1\r\nuSeR-aGeNt:\toriginal\r\n\r\n", "GET / HTTP/1.1\r\nuSeR-aGeNt:\tmobile  \r\n\r\n"},
		{"header and body", "POST / HTTP/1.1\r\nUser-Agent: original\r\n\r\nUser-Agent: secret", "POST / HTTP/1.1\r\nUser-Agent: mobile  \r\n\r\nUser-Agent: secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(tc.request)
			got := replaceUserAgent(original, "mobile")
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected rewrite: %q", got)
				}
			} else if string(got) != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if string(original) != tc.request {
				t.Fatal("input mutated")
			}
		})
	}
}
func TestRewriteMalformedAndFragmentedPackets(t *testing.T) {
	cfg := &Config{TTL: 64, UA: "mobile"}
	for size := 0; size < 40; size++ {
		raw := make([]byte, size)
		if size > 0 {
			raw[0] = 0x45
		}
		rewrite(&godivert.Packet{Raw: raw, PacketLen: uint(size)}, cfg)
	}
	for _, mutate := range []func([]byte){
		func(raw []byte) { raw[0] = 0x4f },
		func(raw []byte) { raw[32] = 0x10 },
		func(raw []byte) { raw[32] = 0xf0 },
		func(raw []byte) { raw[6] = 0x20 },
		func(raw []byte) { raw[7] = 1 },
		func(raw []byte) { raw[2] = 0xff; raw[3] = 0xff },
	} {
		pkt := makeIPv4TCPPacket(80, []byte("GET / HTTP/1.1\r\nUser-Agent: original\r\n\r\n"))
		mutate(pkt.Raw)
		if _, changed := rewrite(pkt, cfg); changed {
			t.Fatal("malformed or fragmented packet rewritten")
		}
	}
}
func TestTTLChecksumMatchesFullRecalculation(t *testing.T) {
	for _, old := range []uint8{1, 32, 64, 128, 255} {
		for _, next := range []uint8{1, 64, 65, 128, 255} {
			pkt := makeIPv4TCPPacket(80, nil)
			pkt.Raw[8] = old
			recomputeIPChecksum(pkt.Raw[:20])
			pkt.Raw[8] = next
			adjustIPChecksum(pkt.Raw[:20], old, next)
			if internetChecksum(pkt.Raw[:20]) != 0 {
				t.Fatalf("invalid checksum for TTL %d -> %d", old, next)
			}
		}
	}
}
func FuzzRewritePacket(f *testing.F) {
	f.Add([]byte{})
	f.Add(makeIPv4TCPPacket(80, []byte("GET / HTTP/1.1\r\nUser-Agent: original\r\n\r\n")).Raw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		original := bytes.Clone(raw)
		pkt := &godivert.Packet{Raw: bytes.Clone(raw), PacketLen: uint(len(raw))}
		rewrite(pkt, &Config{TTL: 64, UA: "mobile"})
		if len(pkt.Raw) != len(original) || pkt.PacketLen != uint(len(raw)) {
			t.Fatal("packet length changed")
		}
	})
}
func FuzzUserAgentRewrite(f *testing.F) {
	f.Add([]byte("POST / HTTP/1.1\r\nUser-Agent: original\r\n\r\nUser-Agent: body"))
	f.Fuzz(func(t *testing.T, payload []byte) {
		original := bytes.Clone(payload)
		got := replaceUserAgent(payload, "mobile")
		if !bytes.Equal(payload, original) {
			t.Fatal("input mutated")
		}
		if got == nil {
			return
		}
		if len(got) != len(payload) {
			t.Fatal("payload length changed")
		}
		end := bytes.Index(payload, []byte("\r\n\r\n"))
		if end < 0 || !bytes.Equal(got[end:], payload[end:]) {
			t.Fatal("HTTP body changed")
		}
	})
}
