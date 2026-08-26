package main

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- rate limiting ---------------------------------------------------------

// rateLimiter is a per-IP token bucket. It exists to keep one misbehaving
// client from monopolizing the database, not to be a full WAF — put a reverse
// proxy in front for anything fancier.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rps     float64
	burst   float64
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps int) *rateLimiter {
	if rps <= 0 {
		return nil
	}
	return &rateLimiter{
		buckets: map[string]*tokenBucket{},
		rps:     float64(rps),
		burst:   float64(rps * 2),
	}
}

func (l *rateLimiter) allow(ip string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Bound memory: when the table grows past a few thousand clients, drop
	// buckets that have been idle long enough to be full again anyway.
	if len(l.buckets) > 4096 {
		for key, bucket := range l.buckets {
			if now.Sub(bucket.last) > time.Minute {
				delete(l.buckets, key)
			}
		}
	}
	bucket, ok := l.buckets[ip]
	if !ok {
		bucket = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[ip] = bucket
	}
	bucket.tokens += now.Sub(bucket.last).Seconds() * l.rps
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.last = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func rateLimitMiddleware(limiter *rateLimiter, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(remoteIP(r)) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- gzip ------------------------------------------------------------------

var compressibleTypes = []string{"text/html", "application/json", "image/svg+xml", "text/css", "text/plain"}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	contentType := w.Header().Get("Content-Type")
	compressible := false
	for _, t := range compressibleTypes {
		if strings.HasPrefix(contentType, t) {
			compressible = true
		}
	}
	if compressible && w.Header().Get("Content-Encoding") == "" && status != http.StatusNoContent {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *gzipResponseWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
	}
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gzw := &gzipResponseWriter{ResponseWriter: w}
		defer gzw.close()
		next.ServeHTTP(gzw, r)
	})
}

// --- page cache -------------------------------------------------------------

// pageCache memoizes successful GET responses for a short TTL. The heavy
// pages aggregate the full events table on every hit; the data only changes
// every ingest cycle, so a briefly stale copy is a fair trade. Admin, health,
// live-status, and upload paths are never cached.
type pageCache struct {
	mu      sync.Mutex
	entries map[string]*cachedPage
	ttl     time.Duration
	max     int
}

type cachedPage struct {
	status      int
	contentType string
	body        []byte
	expires     time.Time
}

func newPageCache(ttl time.Duration) *pageCache {
	if ttl <= 0 {
		return nil
	}
	return &pageCache{entries: map[string]*cachedPage{}, ttl: ttl, max: 512}
}

var uncachedPrefixes = []string{"/admin", "/healthz", "/live", "/api/ingest"}

// maxCacheableBody bounds how large a response the page cache will hold. A
// response that exceeds it must not be cached at all — storing the buffered
// prefix would serve a truncated page for the whole TTL.
const maxCacheableBody = 4 << 20

type cacheRecorder struct {
	http.ResponseWriter
	status   int
	body     []byte
	overflow bool
}

func (r *cacheRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *cacheRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status == http.StatusOK && !r.overflow {
		if len(r.body)+len(p) > maxCacheableBody {
			r.overflow = true
			r.body = nil
		} else {
			r.body = append(r.body, p...)
		}
	}
	return r.ResponseWriter.Write(p)
}

func pageCacheMiddleware(cache *pageCache, next http.Handler) http.Handler {
	if cache == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		for _, prefix := range uncachedPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}
		key := r.URL.RequestURI()
		now := time.Now()
		cache.mu.Lock()
		entry, ok := cache.entries[key]
		if ok && now.Before(entry.expires) {
			cache.mu.Unlock()
			if entry.contentType != "" {
				w.Header().Set("Content-Type", entry.contentType)
			}
			w.Header().Set("X-Cache", "hit")
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}
		if ok {
			delete(cache.entries, key)
		}
		cache.mu.Unlock()

		rec := &cacheRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusOK || rec.overflow || len(rec.body) == 0 {
			return
		}
		cache.mu.Lock()
		if len(cache.entries) >= cache.max {
			for k, v := range cache.entries {
				if now.After(v.expires) {
					delete(cache.entries, k)
				}
			}
			for k := range cache.entries {
				if len(cache.entries) < cache.max {
					break
				}
				delete(cache.entries, k)
			}
		}
		cache.entries[key] = &cachedPage{
			status:      rec.status,
			contentType: rec.Header().Get("Content-Type"),
			body:        rec.body,
			expires:     now.Add(cache.ttl),
		}
		cache.mu.Unlock()
	})
}
