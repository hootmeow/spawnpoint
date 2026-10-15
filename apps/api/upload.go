package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hootmeow/spawnpoint/bflog"
	"github.com/hootmeow/spawnpoint/internal/notify"
	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

// ingestUpload receives a raw BF1942 event log from a remote worker
// (POST body = file bytes, X-Original-Name = file name) and ingests it for
// the server identified by the bearer API key from /admin/servers.
func (s *apiServer) ingestUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.db == nil || s.store == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	key := ""
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		key = strings.TrimSpace(auth[len("bearer "):])
	}
	if key == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="spawnpoint-ingest"`)
		http.Error(w, "server API key required", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()
	var serverID, serverName string
	var enabled bool
	// Keys are stored only as a SHA-256 hash (see migration 0013): hash the
	// presented key and look up by hash so a leaked DB/backup can't be replayed.
	err := s.db.QueryRowContext(ctx, `
		select id::text, name, enabled from servers where api_key_sha256 = $1
	`, hashAPIKey(key)).Scan(&serverID, &serverName, &enabled)
	if err != nil {
		log.Printf("upload auth failed remote=%s", remoteIP(r))
		http.Error(w, "invalid server API key", http.StatusUnauthorized)
		return
	}
	if !enabled {
		http.Error(w, "server is disabled", http.StatusForbidden)
		return
	}

	name := filepath.Base(stripControlChars(strings.TrimSpace(r.Header.Get("X-Original-Name"))))
	if name == "" || name == "." {
		name = fmt.Sprintf("upload-%d.xml", time.Now().Unix())
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".xml" && ext != ".zxml" {
		uploadError(w, http.StatusBadRequest, "X-Original-Name must end in .xml or .zxml")
		return
	}

	maxBytes := int64(envInt("MAX_LOG_FILE_BYTES", 100<<20))
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		uploadError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if int64(len(raw)) > maxBytes {
		uploadError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("log exceeds %d bytes", maxBytes))
		return
	}
	if len(raw) == 0 {
		uploadError(w, http.StatusBadRequest, "empty body")
		return
	}

	decoded := raw
	if ext == ".zxml" {
		reader, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			uploadError(w, http.StatusBadRequest, "invalid zlib stream: "+err.Error())
			return
		}
		decoded, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
		_ = reader.Close()
		if err != nil || int64(len(decoded)) > maxBytes {
			uploadError(w, http.StatusRequestEntityTooLarge, "decompressed log too large or corrupt")
			return
		}
	}
	if !bytes.Contains(decoded, []byte("</bf:log>")) {
		uploadError(w, http.StatusUnprocessableEntity, "log is not complete yet")
		return
	}

	result, err := bflog.Parse(bytes.NewReader(decoded), name)
	if err != nil {
		uploadError(w, http.StatusUnprocessableEntity, "parse: "+err.Error())
		return
	}

	jobID, err := s.store.StartIngestJob(ctx, serverID, "upload:"+name, int64(len(raw)))
	if err != nil {
		log.Printf("upload %s: record ingest job: %v", name, err)
	}
	summary, err := s.store.IngestRawLog(ctx, name, raw, result, postgres.IngestOptions{
		ServerID:   serverID,
		ArchiveDir: archiveDir(),
	})
	if err != nil {
		_ = s.store.FinishIngestJob(ctx, jobID, postgres.IngestJobUpdate{Status: "failed", ErrorMessage: err.Error(), FileSizeBytes: int64(len(raw))})
		// Log the real error (which can carry table/column names and paths)
		// server-side; return a generic message to the caller.
		log.Printf("upload %s: ingest failed: %v", name, err)
		uploadError(w, http.StatusInternalServerError, "ingest failed")
		return
	}
	status := "parsed"
	if summary.AlreadySeen {
		status = "skipped"
	} else {
		notify.RoundComplete(ctx, s.store, s.webhook, summary.RoundIDs, s.roundNotifyWindow)
	}
	_ = s.store.FinishIngestJob(ctx, jobID, postgres.IngestJobUpdate{
		Status:        status,
		SHA256:        summary.SHA256,
		StorageURI:    summary.StorageURI,
		FileSizeBytes: int64(len(raw)),
		Rounds:        summary.Rounds,
		Events:        summary.Events,
		RoundStats:    summary.RoundStats,
	})
	log.Printf("upload %s: server=%s status=%s rounds=%d events=%d", name, serverName, status, summary.Rounds, summary.Events)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": status,
		"sha256": summary.SHA256,
		"rounds": summary.Rounds,
		"events": summary.Events,
	})
}

// hashAPIKey returns the lowercase hex SHA-256 of a server ingest API key.
// Keys are compared and stored only in this hashed form.
func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func archiveDir() string {
	if dir := strings.TrimSpace(os.Getenv("ARCHIVE_DIR")); dir != "" {
		return dir
	}
	return "data/raw-logs"
}

func uploadError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "failed", "error": message})
}

// stripControlChars drops characters an attacker could use to forge extra
// lines in server logs (or elsewhere the value is later displayed/stored):
// X-Original-Name is caller-supplied and otherwise flows straight into
// log.Printf calls and the ingest job's stored label.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x1f || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
