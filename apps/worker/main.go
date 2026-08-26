package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hootmeow/spawnpoint/internal/gamespy"
	"github.com/hootmeow/spawnpoint/internal/notify"
	"github.com/hootmeow/spawnpoint/internal/parser/bflog"
	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"; update.sh
// and setup.sh both pass it as `git describe --tags`. Defaults to "dev" for
// `go run`/unversioned builds.
var version = "dev"

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	var serverDirs serverDirFlags
	var (
		showVersion       = flag.Bool("version", false, "print version and exit")
		databaseURL       = flag.String("database-url", getenv("DATABASE_URL", ""), "PostgreSQL connection URL")
		archiveDir        = flag.String("archive-dir", getenv("ARCHIVE_DIR", "data/raw-logs"), "raw log archive directory")
		serverName        = flag.String("server-name", getenv("SERVER_NAME", "BF1942 Server"), "server display name")
		serverAddress     = flag.String("server-address", getenv("SERVER_ADDRESS", "127.0.0.1"), "server address")
		serverPort        = flag.Int("server-port", getenvInt("SERVER_PORT", 14567), "server game port")
		parseOnly         = flag.Bool("parse-only", false, "parse logs without storing them")
		reingest          = flag.Bool("reingest", false, "purge and re-ingest logs that were already processed")
		watch             = flag.Bool("watch", false, "poll paths and ingest completed logs continuously")
		interval          = flag.Duration("interval", 15*time.Second, "watch polling interval")
		maxFileBytes      = flag.Int64("max-file-bytes", getenvInt64("MAX_LOG_FILE_BYTES", 100<<20), "maximum raw or decompressed log size")
		uploadURL         = flag.String("upload-url", getenv("UPLOAD_URL", ""), "ship logs to a central API instead of a local database (e.g. https://stats.example/api/ingest/upload)")
		uploadKey         = flag.String("upload-key", getenv("UPLOAD_API_KEY", ""), "server ingest API key for -upload-url (from /admin/servers)")
		stateFile         = flag.String("state-file", getenv("WORKER_STATE_FILE", ""), "persist the watch-mode seen-file cache here across restarts (default: alongside -archive-dir); set to 'none' to disable")
		webhookURL        = flag.String("webhook-url", getenv("DISCORD_WEBHOOK_URL", ""), "Discord webhook URL for round-complete and server up/down notifications (unset disables both)")
		serverWatch       = flag.Duration("server-watch-interval", getenvDuration("SERVER_WATCH_INTERVAL", 60*time.Second), "how often -watch mode polls registered servers for up/down notifications")
		roundNotifyWindow = flag.Duration("round-notify-window", getenvDuration("ROUND_NOTIFY_WINDOW", 20*time.Minute), "only send a round-complete notification if the round ended within this long ago (suppresses backlog/reingest spam)")
	)
	flag.Var(&serverDirs, "server-dir", "map a local log directory to a server, repeatable: NAME=/path or NAME@host:port=/path")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if flag.NArg() == 0 && len(serverDirs) == 0 {
		log.Fatal("usage: worker [flags] <path-to-log-or-directory> [...] or worker [flags] -server-dir NAME=/path [...]")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bflog.MaxEvents = int(getenvInt64("MAX_LOG_EVENTS", int64(bflog.MaxEvents)))

	var up *uploader
	if *uploadURL != "" {
		if *uploadKey == "" {
			log.Fatal("-upload-key (or UPLOAD_API_KEY) is required with -upload-url")
		}
		if u, err := url.Parse(*uploadURL); err == nil && strings.EqualFold(u.Scheme, "http") && !isLoopbackHost(u.Hostname()) {
			log.Printf("SECURITY WARNING: -upload-url uses plain http:// (%s); the ingest API key is sent as a bearer token on every upload and will transit in cleartext. Use https:// for any non-loopback target.", *uploadURL)
		}
		up = &uploader{url: *uploadURL, key: *uploadKey, client: &http.Client{Timeout: 2 * time.Minute}}
	}
	if up != nil && len(serverDirs) > 0 {
		log.Fatal("-server-dir is for local database ingest; run one upload worker per server API key")
	}

	var store *postgres.Store
	var err error
	baseOpts := postgres.IngestOptions{
		ServerName:    *serverName,
		ServerAddress: *serverAddress,
		ServerPort:    *serverPort,
		ArchiveDir:    *archiveDir,
		Replace:       *reingest,
	}
	if !*parseOnly && up == nil {
		if *databaseURL == "" {
			log.Fatal("DATABASE_URL, -upload-url, or -parse-only is required")
		}
		store, err = postgres.Open(ctx, *databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer store.Close()
		if len(serverDirs) == 0 || flag.NArg() > 0 {
			baseOpts.ServerID, err = store.EnsureServer(ctx, *serverName, *serverAddress, *serverPort)
			if err != nil {
				log.Fatalf("resolve server %q: %v", *serverName, err)
			}
		}
	}
	sourceSpecs, err := buildSourceSpecs(ctx, store, flag.Args(), serverDirs, baseOpts, *parseOnly || up != nil)
	if err != nil {
		log.Fatal(err)
	}

	// seen remembers each path's last observed size/mtime so watch mode
	// leaves settled files alone instead of re-reading (and re-recording an
	// ingest job for) every archived log on every poll. It's persisted to
	// disk (resolvedStateFile) so a restart doesn't forget every file it
	// already confirmed unchanged and re-verify the entire backlog against
	// the database — on this project's own production install that was
	// writing 5,000+ spurious 'skipped' rows per restart.
	resolvedStateFile := *stateFile
	if resolvedStateFile == "" {
		resolvedStateFile = filepath.Join(filepath.Dir(*archiveDir), "worker-seen-cache.json")
	}
	seen := map[string]fileState{}
	if resolvedStateFile != "none" {
		if loaded, err := loadSeenCache(resolvedStateFile); err != nil {
			log.Printf("state file %s: %v (starting with an empty cache)", resolvedStateFile, err)
		} else {
			seen = loaded
		}
	}

	webhook := notify.NewWebhook(*webhookURL)
	notifier := roundNotifier{webhook: webhook, window: *roundNotifyWindow}
	if *watch && webhook != nil && store != nil {
		go watchServers(ctx, store, webhook, *serverWatch)
	}

	for {
		targets, err := expandSourceTargets(sourceSpecs)
		if err != nil {
			log.Fatal(err)
		}
		if len(targets) == 0 && !*watch {
			log.Fatal("no .xml or .zxml logs found")
		}
		current := map[string]bool{}
		for _, target := range targets {
			path := target.path
			current[path] = true
			info, statErr := os.Lstat(path)
			if statErr != nil {
				log.Printf("%s: %v", path, statErr)
				continue
			}
			if *watch {
				if state, ok := seen[path]; ok && state.Settled && state.Size == info.Size() && state.ModTime.Equal(info.ModTime()) {
					continue
				}
			}
			status, err := processPath(ctx, store, up, path, *parseOnly, *maxFileBytes, postgres.IngestOptions{
				ServerID:      target.opts.ServerID,
				ServerName:    target.opts.ServerName,
				ServerAddress: target.opts.ServerAddress,
				ServerPort:    target.opts.ServerPort,
				ArchiveDir:    target.opts.ArchiveDir,
				Replace:       target.opts.Replace,
			}, notifier)
			if err != nil && (err != errIncompleteLog || !*watch) {
				log.Printf("%s: %v", path, err)
			}
			// A still-growing log stays unsettled and is re-checked next
			// poll; everything else only gets another look if it changes.
			seen[path] = fileState{Size: info.Size(), ModTime: info.ModTime(), Settled: status != statusIncomplete}
		}
		for path := range seen {
			if !current[path] {
				delete(seen, path)
			}
		}
		if resolvedStateFile != "none" {
			if err := saveSeenCache(resolvedStateFile, seen); err != nil {
				log.Printf("state file %s: save failed: %v", resolvedStateFile, err)
			}
		}
		if !*watch {
			break
		}
		select {
		case <-ctx.Done():
			log.Printf("shutting down: finished current poll, exiting")
			return
		case <-time.After(*interval):
		}
	}
}

// watchServers polls every enabled registered server's GameSpy status on
// interval and posts a Discord message on each online/offline transition.
// Only meaningful in -watch mode, which is the only case this is started in.
func watchServers(ctx context.Context, store *postgres.Store, webhook *notify.Webhook, interval time.Duration) {
	known := map[string]bool{} // server id -> last observed online state
	for {
		servers, err := store.ListEnabledServers(ctx)
		if err != nil {
			log.Printf("server watch: list servers: %v", err)
		} else {
			for _, srv := range servers {
				values, err := gamespy.Query(srv.Address, srv.QueryPort, 3*time.Second)
				online := err == nil
				wasOnline, seen := known[srv.ID]
				known[srv.ID] = online
				if !seen || wasOnline == online {
					continue
				}
				if online {
					mapName := values["mapname"]
					if mapName != "" {
						_ = webhook.Send(ctx, fmt.Sprintf("🟢 **%s** is back online (map `%s`)", srv.Name, mapName))
					} else {
						_ = webhook.Send(ctx, fmt.Sprintf("🟢 **%s** is back online", srv.Name))
					}
				} else {
					_ = webhook.Send(ctx, fmt.Sprintf("🔴 **%s** stopped responding to status queries", srv.Name))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

type serverDir struct {
	name    string
	address string
	port    int
	path    string
}

type serverDirFlags []serverDir

func (f *serverDirFlags) String() string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, len(*f))
	for _, entry := range *f {
		parts = append(parts, entry.name+"="+entry.path)
	}
	return strings.Join(parts, ",")
}

func (f *serverDirFlags) Set(value string) error {
	entry, err := parseServerDir(value, getenv("SERVER_ADDRESS", "127.0.0.1"), getenvInt("SERVER_PORT", 14567))
	if err != nil {
		return err
	}
	*f = append(*f, entry)
	return nil
}

func parseServerDir(value string, defaultAddress string, defaultPort int) (serverDir, error) {
	left, path, ok := strings.Cut(value, "=")
	if !ok {
		return serverDir{}, fmt.Errorf("server-dir must be NAME=/path or NAME@host:port=/path")
	}
	left = strings.TrimSpace(left)
	path = strings.TrimSpace(path)
	if left == "" || path == "" {
		return serverDir{}, fmt.Errorf("server-dir requires both server name and path")
	}
	entry := serverDir{name: left, address: defaultAddress, port: defaultPort, path: path}
	if at := strings.LastIndex(left, "@"); at >= 0 {
		entry.name = strings.TrimSpace(left[:at])
		endpoint := strings.TrimSpace(left[at+1:])
		if entry.name == "" || endpoint == "" {
			return serverDir{}, fmt.Errorf("server-dir requires NAME@host:port before =")
		}
		entry.address = endpoint
		if colon := strings.LastIndex(endpoint, ":"); colon > 0 {
			if parsed, err := strconv.Atoi(endpoint[colon+1:]); err == nil && parsed > 0 {
				entry.address = endpoint[:colon]
				entry.port = parsed
			}
		}
	}
	return entry, nil
}

type sourceSpec struct {
	path string
	opts postgres.IngestOptions
}

type sourceTarget struct {
	path string
	opts postgres.IngestOptions
}

func buildSourceSpecs(ctx context.Context, store *postgres.Store, args []string, serverDirs []serverDir, baseOpts postgres.IngestOptions, skipEnsure bool) ([]sourceSpec, error) {
	if len(serverDirs) == 0 {
		specs := make([]sourceSpec, 0, len(args))
		for _, path := range args {
			specs = append(specs, sourceSpec{path: path, opts: baseOpts})
		}
		return specs, nil
	}
	specs := make([]sourceSpec, 0, len(serverDirs)+len(args))
	for _, entry := range serverDirs {
		opts := baseOpts
		opts.ServerID = ""
		opts.ServerName = entry.name
		opts.ServerAddress = entry.address
		opts.ServerPort = entry.port
		if !skipEnsure {
			serverID, err := store.EnsureServer(ctx, opts.ServerName, opts.ServerAddress, opts.ServerPort)
			if err != nil {
				return nil, fmt.Errorf("resolve server %q: %w", opts.ServerName, err)
			}
			opts.ServerID = serverID
		}
		specs = append(specs, sourceSpec{path: entry.path, opts: opts})
	}
	for _, path := range args {
		specs = append(specs, sourceSpec{path: path, opts: baseOpts})
	}
	return specs, nil
}

func expandSourceTargets(specs []sourceSpec) ([]sourceTarget, error) {
	var targets []sourceTarget
	for _, spec := range specs {
		paths, err := expandPaths([]string{spec.path})
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			targets = append(targets, sourceTarget{path: path, opts: spec.opts})
		}
	}
	return targets, nil
}

var errIncompleteLog = fmt.Errorf("log is not complete yet")

const (
	statusIncomplete = "incomplete"
	statusFailed     = "failed"
	statusSkipped    = "skipped"
	statusParsed     = "parsed"
)

// uploader ships raw logs to a central API over HTTPS instead of writing to
// a local database; the receiving side parses, archives, and dedupes.
type uploader struct {
	url    string
	key    string
	client *http.Client
}

func (u *uploader) send(ctx context.Context, path string, raw []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+u.key)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Original-Name", filepath.Base(path))
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("upload failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Status == "" {
		return statusParsed, nil
	}
	return result.Status, nil
}

// recordFailedJob makes real failures visible on the admin dashboard. It is
// not used for incomplete (still-growing) logs, which are a normal condition.
func recordFailedJob(ctx context.Context, store *postgres.Store, serverID string, path string, size int64, cause error) {
	if store == nil {
		return
	}
	jobID, err := store.StartIngestJob(ctx, serverID, path, size)
	if err != nil {
		log.Printf("%s: record ingest job: %v", path, err)
		return
	}
	_ = store.FinishIngestJob(ctx, jobID, postgres.IngestJobUpdate{Status: statusFailed, ErrorMessage: cause.Error(), FileSizeBytes: size})
}

// roundNotifier bundles the webhook and freshness window processPath needs
// to fire a round-complete notification after a successful ingest; the
// message-building logic itself lives in internal/notify so apps/api's
// upload endpoint (the central-ingest path for remote workers) can reuse it.
type roundNotifier struct {
	webhook *notify.Webhook
	window  time.Duration
}

func (n roundNotifier) notify(ctx context.Context, store *postgres.Store, roundIDs []string) {
	notify.RoundComplete(ctx, store, n.webhook, roundIDs, n.window)
}

func processPath(ctx context.Context, store *postgres.Store, up *uploader, path string, parseOnly bool, maxFileBytes int64, opts postgres.IngestOptions, notifier roundNotifier) (string, error) {
	size := fileSize(path)
	if err := validateLogPath(path, maxFileBytes); err != nil {
		recordFailedJob(ctx, store, opts.ServerID, path, size, err)
		return statusFailed, err
	}
	raw, parsedBytes, err := readLog(path, maxFileBytes)
	if err != nil {
		recordFailedJob(ctx, store, opts.ServerID, path, size, err)
		return statusFailed, err
	}
	if !isCompleteLog(parsedBytes) {
		return statusIncomplete, errIncompleteLog
	}

	if up != nil {
		status, err := up.send(ctx, path, raw)
		if err != nil {
			return statusFailed, err
		}
		fmt.Printf("uploaded %s: status=%s\n", path, status)
		return status, nil
	}

	result, err := bflog.Parse(bytes.NewReader(parsedBytes), path)
	if err != nil {
		recordFailedJob(ctx, store, opts.ServerID, path, size, err)
		return statusFailed, err
	}

	if parseOnly {
		fmt.Printf("parsed %s: rounds=%d events=%d roundstats=%d\n", path, result.Rounds, len(result.Events), len(result.RoundStats))
		return statusParsed, nil
	}

	jobID := ""
	if id, err := store.StartIngestJob(ctx, opts.ServerID, path, size); err != nil {
		log.Printf("%s: record ingest job: %v", path, err)
	} else {
		jobID = id
	}
	summary, err := store.IngestRawLog(ctx, path, raw, result, opts)
	if err != nil {
		_ = store.FinishIngestJob(ctx, jobID, postgres.IngestJobUpdate{Status: statusFailed, ErrorMessage: err.Error(), FileSizeBytes: size})
		return statusFailed, err
	}
	status := statusParsed
	if summary.AlreadySeen {
		status = statusSkipped
		fmt.Printf("skipped %s: already ingested sha256=%s\n", path, summary.SHA256)
	} else {
		fmt.Printf("ingested %s: rounds=%d events=%d roundstats=%d archive=%s\n", path, summary.Rounds, summary.Events, summary.RoundStats, summary.StorageURI)
		notifier.notify(ctx, store, summary.RoundIDs)
	}
	_ = store.FinishIngestJob(ctx, jobID, postgres.IngestJobUpdate{
		Status:        status,
		SHA256:        summary.SHA256,
		StorageURI:    summary.StorageURI,
		FileSizeBytes: size,
		Rounds:        summary.Rounds,
		Events:        summary.Events,
		RoundStats:    summary.RoundStats,
	})
	return status, nil
}

func isCompleteLog(data []byte) bool {
	return bytes.Contains(data, []byte("</bf:log>"))
}

// fileState is watch mode's per-path memory of the last observed size/mtime,
// used to skip re-processing (and re-recording an ingest job for) settled
// files. Persisted as JSON via loadSeenCache/saveSeenCache.
type fileState struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Settled bool      `json:"settled"`
}

func loadSeenCache(path string) (map[string]fileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]fileState{}, nil
		}
		return map[string]fileState{}, err
	}
	var seen map[string]fileState
	if err := json.Unmarshal(data, &seen); err != nil {
		// A corrupt or foreign-format state file just means a slower first
		// poll (full re-verification), not a fatal error.
		return map[string]fileState{}, err
	}
	return seen, nil
}

// saveSeenCache writes via a temp file + rename so a process killed
// mid-write never leaves a corrupt state file behind.
func saveSeenCache(path string, seen map[string]fileState) error {
	data, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func expandPaths(inputs []string) ([]string, error) {
	var paths []string
	for _, input := range inputs {
		info, err := os.Lstat(input)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: symlink inputs are not allowed", input)
		}
		if !info.IsDir() {
			if isLogPath(input) {
				paths = append(paths, input)
			}
			continue
		}
		err = filepath.WalkDir(input, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() || !isLogPath(path) {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func isLogPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".xml" || ext == ".zxml"
}

func validateLogPath(path string, maxFileBytes int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink logs are not allowed")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if maxFileBytes > 0 && info.Size() > maxFileBytes {
		return fmt.Errorf("log is too large: %d bytes > %d", info.Size(), maxFileBytes)
	}
	return nil
}

func readLog(path string, maxFileBytes int64) ([]byte, []byte, error) {
	raw, err := readLimitedFile(path, maxFileBytes)
	if err != nil {
		return nil, nil, err
	}
	if strings.ToLower(filepath.Ext(path)) != ".zxml" {
		return raw, raw, nil
	}
	reader, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	decoded, err := readLimited(reader, maxFileBytes)
	if err != nil {
		return nil, nil, err
	}
	return raw, decoded, nil
}

func readLimitedFile(path string, maxFileBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, maxFileBytes)
}

func readLimited(reader io.Reader, maxFileBytes int64) ([]byte, error) {
	if maxFileBytes <= 0 {
		return io.ReadAll(reader)
	}
	limited := io.LimitReader(reader, maxFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxFileBytes {
		return nil, fmt.Errorf("log is too large after read/decompression: %d bytes > %d", len(data), maxFileBytes)
	}
	return data, nil
}

func fileSize(path string) int64 {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

func getenv(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

func getenvInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	var parsed int64
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
