package connectivity

import (
	"encoding/base64"
	"encoding/binary"
)

// BuildAuthFrame constructs the SAP proprietary SOCKS5 sub-negotiation frame
// for auth method 0x80.
//
// Wire layout:
//
//	0x01                       (1 byte)  sub-negotiation version
//	uint32 BE  len(jwt)        (4 bytes) JWT byte length
//	[]byte     jwt
//	uint8      len(locBase64)  (1 byte)  0 when locationID == ""
//	[]byte     locBase64       (variable) base64-encoded locationID
//
// The function is exported so frame_test.go can verify the byte layout
// independently of the SOCKS5 handshake.
func BuildAuthFrame(jwt, locationID string) []byte {
	jwtBytes := []byte(jwt)

	var locPart []byte
	if locationID == "" {
		locPart = []byte{0x00}
	} else {
		b64 := []byte(base64.StdEncoding.EncodeToString([]byte(locationID)))
		locPart = make([]byte, 1+len(b64))
		locPart[0] = byte(len(b64))
		copy(locPart[1:], b64)
	}

	buf := make([]byte, 1+4+len(jwtBytes)+len(locPart))
	off := 0
	buf[off] = 0x01 // sub-negotiation version
	off++
	binary.BigEndian.PutUint32(buf[off:], uint32(len(jwtBytes)))
	off += 4
	copy(buf[off:], jwtBytes)
	off += len(jwtBytes)
	copy(buf[off:], locPart)

	return buf
}

// BuildConnect constructs the SOCKS5 CONNECT request frame.
//
// Wire layout (RFC 1928 §4):
//
//	0x05              VER
//	0x01              CMD=CONNECT
//	0x00              RSV
//	0x03              ATYP=DOMAINNAME
//	uint8  len(host)
//	[]byte host
//	uint16 BE port
func BuildConnect(host string, port uint16) []byte {
	hostBytes := []byte(host)
	buf := make([]byte, 4+1+len(hostBytes)+2)
	buf[0] = 0x05
	buf[1] = 0x01
	buf[2] = 0x00
	buf[3] = 0x03
	buf[4] = byte(len(hostBytes))
	copy(buf[5:], hostBytes)
	binary.BigEndian.PutUint16(buf[5+len(hostBytes):], port)
	return buf
}
