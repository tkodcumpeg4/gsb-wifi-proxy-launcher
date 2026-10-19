package main

import "encoding/binary"

// isClientHelloRecord matches a TLS handshake record carrying a ClientHello:
// 16 03 xx LL LL 01 ...
func isClientHelloRecord(d []byte) bool {
	return len(d) > 6 && d[0] == 0x16 && d[1] == 0x03 && d[5] == 0x01
}

// mutateClientHello obfuscates the first client→server chunk of a tunnel.
//
// "version" (Python parity): rewrite the record-layer version to 03 04.
// Servers ignore legacy_record_version, but signature-based DPI misses it.
//
// "split": cut the ClientHello into two TLS records with the boundary in the
// middle of the SNI host name. Fragmenting handshake messages is legal TLS,
// and because records survive the proxy's TCP re-segmentation, a DPI box
// behind Squid never sees the whole SNI in one record.
func mutateClientHello(d []byte, mode string) []byte {
	if !isClientHelloRecord(d) {
		return d
	}
	if mode == tlsModeSplit {
		if out := splitClientHello(d); out != nil {
			return out
		}
	}
	out := append([]byte(nil), d...)
	out[2] = 0x04
	return out
}

func splitClientHello(d []byte) []byte {
	recLen := int(binary.BigEndian.Uint16(d[3:5]))
	if len(d) < 5+recLen || recLen < 8 {
		return nil
	}
	payload := d[5 : 5+recLen]
	cut := recLen / 2
	if off, n := sniOffset(payload); n > 1 {
		cut = off + n/2
	}
	out := make([]byte, 0, len(d)+5)
	out = appendRecord(out, d[2], payload[:cut])
	out = appendRecord(out, d[2], payload[cut:])
	return append(out, d[5+recLen:]...)
}

func appendRecord(out []byte, ver byte, frag []byte) []byte {
	out = append(out, 0x16, 0x03, ver, byte(len(frag)>>8), byte(len(frag)))
	return append(out, frag...)
}

// sniOffset returns the offset and length of the server_name host inside a
// ClientHello handshake message, or (0, 0) if it cannot be located.
func sniOffset(p []byte) (int, int) {
	// type(1) len(3) version(2) random(32)
	i := 38
	if len(p) < i+1 {
		return 0, 0
	}
	i += 1 + int(p[i]) // session id
	if len(p) < i+2 {
		return 0, 0
	}
	i += 2 + int(binary.BigEndian.Uint16(p[i:])) // cipher suites
	if len(p) < i+1 {
		return 0, 0
	}
	i += 1 + int(p[i]) // compression methods
	if len(p) < i+2 {
		return 0, 0
	}
	end := i + 2 + int(binary.BigEndian.Uint16(p[i:]))
	i += 2
	if end > len(p) {
		end = len(p)
	}
	for i+4 <= end {
		typ := binary.BigEndian.Uint16(p[i:])
		l := int(binary.BigEndian.Uint16(p[i+2:]))
		i += 4
		if typ == 0 && i+5 <= end { // server_name: list(2) type(1) len(2) name
			n := int(binary.BigEndian.Uint16(p[i+3:]))
			if i+5+n <= end {
				return i + 5, n
			}
			return 0, 0
		}
		i += l
	}
	return 0, 0
}
