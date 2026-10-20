package main

import (
	"bytes"
	"encoding/binary"
	"github.com/williamfhe/godivert"
)

// Validate lengths before touching third-party header parsers. Fragmented IP
// datagrams and malformed transport headers must never be treated as HTTP.
func rewrite(pkt *godivert.Packet, cfg *Config) (bool, bool) {
	if pkt == nil || cfg == nil || len(pkt.Raw) < 20 || pkt.Raw[0]>>4 != 4 {
		return false, false
	}
	raw := pkt.Raw
	ipLen := int(raw[0]&15) * 4
	total := int(binary.BigEndian.Uint16(raw[2:4]))
	if ipLen < 20 || ipLen > len(raw) || total < ipLen || total > len(raw) {
		return false, false
	}
	ttlChanged := raw[8] != cfg.TTL
	if ttlChanged {
		oldTTL := raw[8]
		raw[8] = cfg.TTL
		adjustIPChecksum(raw[:ipLen], oldTTL, cfg.TTL)
	}
	if raw[9] != 6 || binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0 || total-ipLen < 20 {
		return ttlChanged, false
	}
	tcpLen := int(raw[ipLen+12]>>4) * 4
	if tcpLen < 20 || ipLen+tcpLen >= total {
		return ttlChanged, false
	}
	payload := raw[ipLen+tcpLen : total]
	replacement := replaceUserAgent(payload, cfg.UA)
	if replacement == nil || bytes.Equal(payload, replacement) {
		return ttlChanged, false
	}
	copy(payload, replacement) // always equal length; never change TCP sequence space
	return ttlChanged, true
}

func looksLikeHTTPRequest(payload []byte) bool {
	end := bytes.Index(payload, []byte("\r\n"))
	if end < 0 {
		return false
	}
	parts := bytes.Split(payload[:end], []byte(" "))
	if len(parts) != 3 || len(parts[1]) == 0 {
		return false
	}
	for _, ch := range parts[1] {
		if ch <= 32 || ch == 127 {
			return false
		}
	}
	if !bytes.Equal(parts[2], []byte("HTTP/1.1")) && !bytes.Equal(parts[2], []byte("HTTP/1.0")) {
		return false
	}
	switch string(parts[0]) {
	case "GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "PATCH", "CONNECT", "TRACE":
		return true
	}
	return false
}

func headerToken(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || bytes.IndexByte([]byte("!#$%&'*+-.^_`|~"), ch) >= 0
}

// Only rewrite a complete, syntactically valid HTTP/1 header block. A segmented
// header is passed through unchanged; this is deliberately not TCP reassembly.
func replaceUserAgent(payload []byte, newUA string) []byte {
	if !looksLikeHTTPRequest(payload) || newUA == "" {
		return nil
	}
	for i := range newUA {
		if newUA[i] < 32 || newUA[i] > 126 {
			return nil
		}
	}
	end := bytes.Index(payload, []byte("\r\n\r\n"))
	if end < 0 {
		return nil
	}
	start := bytes.Index(payload, []byte("\r\n")) + 2
	valueStart, valueEnd := -1, -1
	for start < end {
		lineLen := bytes.Index(payload[start:end+2], []byte("\r\n"))
		if lineLen < 0 {
			return nil
		}
		line := payload[start : start+lineLen]
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			return nil
		}
		for _, ch := range line[:colon] {
			if !headerToken(ch) {
				return nil
			}
		}
		for _, ch := range line[colon+1:] {
			if ch < 32 && ch != '\t' || ch == 127 {
				return nil
			}
		}
		if bytes.EqualFold(line[:colon], []byte("User-Agent")) {
			if valueStart >= 0 {
				return nil
			} // ambiguous duplicate header
			valueStart = start + colon + 1
			for valueStart < start+lineLen && (payload[valueStart] == ' ' || payload[valueStart] == '\t') {
				valueStart++
			}
			valueEnd = start + lineLen
		}
		start += lineLen + 2
	}
	if valueStart < 0 || valueStart == valueEnd {
		return nil
	}
	out := bytes.Clone(payload)
	for i := valueStart; i < valueEnd; i++ {
		out[i] = ' '
	}
	copy(out[valueStart:valueEnd], newUA) // retain stable truncation/padding policy
	return out
}

func adjustIPChecksum(hdr []byte, oldTTL, newTTL uint8) {
	if len(hdr) < 12 {
		return
	}
	// RFC 1624: HC' = ~(~HC + ~m + m'), with protocol byte unchanged.
	checksum := uint32(^binary.BigEndian.Uint16(hdr[10:12]))
	checksum += uint32(^(uint16(oldTTL) << 8)) + uint32(uint16(newTTL)<<8)
	for checksum > 0xffff {
		checksum = (checksum & 0xffff) + (checksum >> 16)
	}
	binary.BigEndian.PutUint16(hdr[10:12], ^uint16(checksum))
}
func recomputeIPChecksum(hdr []byte) uint16 {
	if len(hdr) < 20 {
		return 0
	}
	hdr[10], hdr[11] = 0, 0
	sum := internetChecksum(hdr)
	binary.BigEndian.PutUint16(hdr[10:12], sum)
	return sum
}
func internetChecksum(data []byte) uint16 {
	var sum uint32
	i := 0
	for ; i+1 < len(data); i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if i < len(data) {
		sum += uint32(data[i]) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
