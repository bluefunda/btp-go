package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/sshclient"
	"github.com/pkg/sftp"
)

type countResponse struct {
	Destination string `json:"destination"`
	RemotePath  string `json:"remotePath"`
	Count       int    `json:"count"`
	ElapsedMs   int64  `json:"elapsedMs"`
}

type errResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// sftpCountHandler returns an http.HandlerFunc that counts files on a remote
// SFTP server using sshclient.Dial — the high-level wrapper that handles port
// parsing, SSH config assembly from destination properties, and retry logic.
//
// Compare with examples/sftp-count which wires raw SSH for the same result.
func sftpCountHandler(destClient *destination.Client, dialer *connectivity.Dialer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		destName := r.URL.Query().Get("destination")
		if destName == "" {
			writeJSON(w, http.StatusBadRequest, errResponse{Error: "'destination' query param is required"})
			return
		}

		dest, err := destClient.Find(ctx, destName)
		if err != nil {
			slog.ErrorContext(ctx, "destination lookup", "destination", destName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("destination lookup: %v", err)})
			return
		}

		// sshclient.Dial handles port parsing, SSH config from dest.User /
		// dest.Properties["sshKey"] / dest.Properties["Password"], and retry
		// on transient MaxStartups rejections from sshd.
		sshc, err := sshclient.Dial(ctx, sshclient.Config{
			Dialer: dialer,
			RetryOpts: sshclient.RetryOpts{
				MaxAttempts: 4,
				BaseDelay:   200 * time.Millisecond,
				Jitter:      true,
			},
		}, dest)
		if err != nil {
			slog.ErrorContext(ctx, "ssh dial", "destination", destName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("ssh dial: %v", err)})
			return
		}
		defer sshc.Close()

		sftpClient, err := sftp.NewClient(sshc)
		if err != nil {
			slog.ErrorContext(ctx, "sftp client", "destination", destName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errResponse{Error: fmt.Sprintf("sftp client: %v", err)})
			return
		}
		defer sftpClient.Close()

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

		writeJSON(w, http.StatusOK, countResponse{
			Destination: destName,
			RemotePath:  remotePath,
			Count:       len(entries),
			ElapsedMs:   elapsed,
		})
	}
}
