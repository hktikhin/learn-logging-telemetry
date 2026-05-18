package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"time"

	"boot.dev/linko/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

var httpRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests.",
	},
	[]string{"method", "path", "status"},
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
		}

		next.ServeHTTP(rec, r)

		path := r.URL.Path
		method := r.Method
		status := strconv.Itoa(rec.status)

		httpRequestsTotal.
			WithLabelValues(method, path, status).
			Inc()
	})
}

func redactIP(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// If splitting fails, the string might just be an IP without a port.
		host = address
		port = ""
	}

	ip := net.ParseIP(host)
	// Check if it is a valid IP and specifically an IPv4 address.
	// ip.To4() returns a 4-byte representation if valid, or nil if IPv6/invalid.
	if ip == nil || ip.To4() == nil {
		return address
	}

	// Extract the 4-byte IPv4 slice
	ipv4 := ip.To4()

	// Rebuild the masked IP string cleanly using a strings.Builder for performance.
	maskedHost := fmt.Sprintf("%d.%d.%d.*", ipv4[0], ipv4[1], ipv4[2])

	// Append the port back if it was originally present
	if port != "" {
		return net.JoinHostPort(fmt.Sprintf("%d.%d.%d.*", ipv4[0], ipv4[1], ipv4[2]), port)
	}
	return maskedHost
}

func httpError(ctx context.Context, w http.ResponseWriter, err error, status int) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	switch status {
	case http.StatusUnauthorized, // 401
		http.StatusForbidden,           // 403
		http.StatusInternalServerError: // 500
		http.Error(w, http.StatusText(status), status)
	default:
		http.Error(w, err.Error(), status)
	}
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	// Replace w.Write
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	// Replace w.WriteHeader
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	// Replace r.body.read
	// 1. Delegate the actual data fetching to the underlying ReadCloser.
	// It copies data into the buffer 'p' and returns how many bytes were moved.
	n, err := r.ReadCloser.Read(p)

	// 2. Intercept and record the number of bytes read so far for monitoring/metrics.
	r.bytesRead += n

	// 3. Pass through the count 'n' and any error (like EOF) to the original caller
	// so they know exactly how much data in 'p' is valid to use.
	return n, err
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lc := &LogContext{}
			r = r.WithContext(context.WithValue(r.Context(), logContextKey, lc))
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			spyWriter := &spyResponseWriter{ResponseWriter: w}
			r.Body = spyReader
			start := time.Now()
			next.ServeHTTP(spyWriter, r)
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", redactIP(r.RemoteAddr)),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
			}
			if lc.Username != "" {
				attrs = append(attrs, slog.String("user", lc.Username))
			}
			if lc.Error != nil {
				attrs = append(attrs, slog.String("error", lc.Error.Error()))
			}

			if request_id := w.Header().Get("X-Request-ID"); request_id != "" {
				attrs = append(attrs, slog.String("request_id", request_id))
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "Served request", attrs...)
		})
	}
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = rand.Text()
		}
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r)
	})
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: requestLogger(logger)(requestIDMiddleware(metricsMiddleware(mux))),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)
	mux.Handle("GET /debug/pprof/", s.authMiddleware(http.HandlerFunc(pprof.Index)))
	mux.Handle("GET /debug/pprof/profile", s.authMiddleware(http.HandlerFunc(pprof.Profile)))

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp4", s.httpServer.Addr)
	if err != nil {
		return err
	}
	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost%s", s.httpServer.Addr))
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	s.logger.Debug("Linko is shutting down")
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
