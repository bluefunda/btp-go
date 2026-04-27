package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// countResponse is the JSON shape returned by GET /sftp/count.
type countResponse struct {
	Destination string `json:"destination"`
	RemotePath  string `json:"remotePath"`
	Count       int    `json:"count"`
	ElapsedMs   int64  `json:"elapsedMs"`
}

// errResponse is returned when the handler encounters an error.
type errResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// sftpCountHandler returns an http.HandlerFunc that:
//  1. Looks up the destination by name from the Destination Service.
//  2. Parses dest.Port as uint16.
//  3. Dials through the SOCKS5 proxy via the Connectivity Service.
//  4. Establishes an SSH connection using credentials from dest.Properties.
//  5. Opens an SFTP client and reads the directory at dest.Properties["RemotePath"].
//  6. Returns the file count as JSON.
func sftpCountHandler(destClient *destination.Client, dialer *connectivity.Dialer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		destName := r.URL.Query().Get("destination")
		if destName == "" {
			writeJSON(w, http.StatusBadRequest, errResponse{Error: "'destination' query param is required"})
			return
		}

		// Step 1: Resolve the destination.
		dest, err := destClient.Find(ctx, destName)
		if err != nil {
			slog.ErrorContext(ctx, "destination lookup", "destination", destName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("destination lookup: %v", err)})
			return
		}

		// Step 2: Parse the port.
		port64, err := strconv.ParseUint(dest.Port, 10, 16)
		if err != nil {
			slog.ErrorContext(ctx, "parse port", "port", dest.Port, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("invalid port %q: %v", dest.Port, err)})
			return
		}

		// Step 3: Dial through SOCKS5 proxy.
		conn, err := dialer.Dial(ctx, dest.Host, uint16(port64), dest.CloudConnectorLocationID)
		if err != nil {
			slog.ErrorContext(ctx, "socks5 dial", "host", dest.Host, "port", dest.Port, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("socks5 dial: %v", err)})
			return
		}
		defer conn.Close()

		// Step 4: Build ssh.ClientConfig.
		sshCfg, err := buildSSHConfig(dest)
		if err != nil {
			slog.ErrorContext(ctx, "build ssh config", "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("ssh config: %v", err)})
			return
		}

		// Step 5: Establish SSH connection over the SOCKS5 tunnel.
		hostPort := net.JoinHostPort(dest.Host, dest.Port)
		sshConn, chans, reqs, err := ssh.NewClientConn(conn, hostPort, sshCfg)
		if err != nil {
			slog.ErrorContext(ctx, "ssh connect", "addr", hostPort, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("ssh connect: %v", err)})
			return
		}
		sshClient := ssh.NewClient(sshConn, chans, reqs)
		defer sshClient.Close()

		// Step 6: Open SFTP client.
		sftpClient, err := sftp.NewClient(sshClient)
		if err != nil {
			slog.ErrorContext(ctx, "sftp client", "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("sftp client: %v", err)})
			return
		}
		defer sftpClient.Close()

		// Step 7: ReadDir and count.
		remotePath := dest.Properties["RemotePath"]
		if remotePath == "" {
			remotePath = "."
		}
		entries, err := sftpClient.ReadDir(remotePath)
		if err != nil {
			slog.ErrorContext(ctx, "sftp readdir", "path", remotePath, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("sftp readdir: %v", err)})
			return
		}

		elapsed := time.Since(start).Milliseconds()
		slog.InfoContext(ctx, "sftp count", "destination", destName, "remotePath", remotePath,
			"count", len(entries), "elapsedMs", elapsed)

		// Step 8: Return JSON.
		writeJSON(w, http.StatusOK, countResponse{
			Destination: destName,
			RemotePath:  remotePath,
			Count:       len(entries),
			ElapsedMs:   elapsed,
		})
	}
}

// buildSSHConfig constructs an ssh.ClientConfig from the destination properties.
// It supports:
//   - Key-based auth: dest.Properties["sshKey"] (PEM-encoded private key)
//   - Password auth: dest.Properties["Password"]
func buildSSHConfig(dest *destination.Destination) (*ssh.ClientConfig, error) {
	user := dest.Properties["User"]
	if user == "" {
		user = "root"
	}

	var authMethods []ssh.AuthMethod

	// Prefer key-based auth.
	if key := dest.Properties["sshKey"]; key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("parse ssh private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Fall back to password auth.
	if pass := dest.Properties["Password"]; pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH auth method available (set sshKey or Password in destination properties)")
	}

	// IMPORTANT: InsecureIgnoreHostKey() skips host key verification.
	// TODO: replace with a known-hosts-based callback before going to production.
	slog.Warn("SSH host key verification is disabled — set a real HostKeyCallback before production use")

	return &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}, nil
}
