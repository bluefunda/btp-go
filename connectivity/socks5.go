package connectivity

import (
	"context"
	"fmt"
	"io"
	"net"
)

// StageError carries the name of the SOCKS5 handshake stage that failed,
// allowing callers to use errors.As to distinguish dial/greeting/auth/connect
// failures.
type StageError struct {
	// Stage is one of: "dial", "greeting", "auth", "connect".
	Stage string
	// Err is the underlying error.
	Err error
}

// Error implements the error interface.
func (e *StageError) Error() string { return fmt.Sprintf("socks5 %s: %v", e.Stage, e.Err) }

// Unwrap returns the underlying error for errors.Is/As chaining.
func (e *StageError) Unwrap() error { return e.Err }

func stageErr(stage string, err error) error {
	return &StageError{Stage: stage, Err: err}
}

// REPMessage converts a SOCKS5 REP byte into a human-readable description.
// It covers all eight codes defined by RFC 1928 §6 and returns a formatted
// hex string for unrecognised codes.
func REPMessage(rep byte) string {
	switch rep {
	case 0x00:
		return "succeeded"
	case 0x01:
		return "general failure"
	case 0x02:
		return "not allowed by ruleset"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	default:
		return fmt.Sprintf("unknown REP=0x%02X", rep)
	}
}

// greeting sends the SOCKS5 client greeting and returns the negotiated method
// byte chosen by the server.
func greeting(conn net.Conn, method byte) (byte, error) {
	frame := []byte{0x05, 0x01, method}
	if _, err := conn.Write(frame); err != nil {
		return 0, fmt.Errorf("write greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return 0, fmt.Errorf("read greeting reply: %w", err)
	}
	if reply[0] != 0x05 {
		return 0, fmt.Errorf("unexpected SOCKS version 0x%02X in greeting reply", reply[0])
	}
	if reply[1] == 0xFF {
		return 0, fmt.Errorf("proxy rejected all proposed auth methods")
	}
	return reply[1], nil
}

// sapAuth sends the SAP 0x80 sub-negotiation frame and reads the 2-byte ack.
func sapAuth(conn net.Conn, jwt, locationID string) error {
	frame := BuildAuthFrame(jwt, locationID)
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("write auth frame: %w", err)
	}
	status := make([]byte, 2)
	if _, err := io.ReadFull(conn, status); err != nil {
		return fmt.Errorf("read auth reply: %w", err)
	}
	if status[0] != 0x01 || status[1] != 0x00 {
		return fmt.Errorf("auth rejected (status=0x%02X 0x%02X)", status[0], status[1])
	}
	return nil
}

// connect sends the SOCKS5 CONNECT request and reads the reply.
func connect(conn net.Conn, host string, port uint16) error {
	if len(host) > 255 {
		return fmt.Errorf("hostname too long (%d bytes)", len(host))
	}
	req := BuildConnect(host, port)
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("write CONNECT: %w", err)
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("read CONNECT reply header: %w", err)
	}
	if header[0] != 0x05 {
		return fmt.Errorf("unexpected SOCKS version 0x%02X in CONNECT reply", header[0])
	}

	// Drain bound address+port.
	if err := drainBoundAddr(conn, header[3]); err != nil {
		return fmt.Errorf("drain bound addr: %w", err)
	}

	rep := header[1]
	if rep != 0x00 {
		return fmt.Errorf("CONNECT rejected: %s (rep=0x%02X)", REPMessage(rep), rep)
	}
	return nil
}

// drainBoundAddr discards the bound address and port bytes from the CONNECT reply.
func drainBoundAddr(conn net.Conn, atyp byte) error {
	var addrLen int
	switch atyp {
	case 0x01: // IPv4
		addrLen = 4
	case 0x04: // IPv6
		addrLen = 16
	case 0x03: // domain name
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return err
		}
		addrLen = int(lenBuf[0])
	default:
		return fmt.Errorf("unknown ATYP=0x%02X in CONNECT reply", atyp)
	}
	discard := make([]byte, addrLen+2)
	_, err := io.ReadFull(conn, discard)
	return err
}

// handshake performs the complete SOCKS5 greeting → optional auth → CONNECT
// sequence on an already-connected TCP socket.
func handshake(ctx context.Context, conn net.Conn, ts TokenSource, virtualHost string, virtualPort uint16, locationID string) error {
	// Set deadline from context if present.
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl) //nolint:errcheck
	}

	// Propose method 0x80 when a TokenSource is available, else 0x00.
	var proposedMethod byte = 0x00
	if ts != nil {
		proposedMethod = 0x80
	}

	method, err := greeting(conn, proposedMethod)
	if err != nil {
		return stageErr("greeting", err)
	}

	switch method {
	case 0x80:
		// SAP proprietary auth — fetch token then send auth frame.
		jwt, err := ts.Token(ctx)
		if err != nil {
			return stageErr("auth", fmt.Errorf("token: %w", err))
		}
		if err := sapAuth(conn, jwt, locationID); err != nil {
			return stageErr("auth", err)
		}
	case 0x00:
		// No authentication required.
	default:
		return stageErr("greeting", fmt.Errorf("unexpected auth method 0x%02X chosen by proxy", method))
	}

	if err := connect(conn, virtualHost, virtualPort); err != nil {
		return stageErr("connect", err)
	}
	return nil
}
