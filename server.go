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
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

type LogContext struct {
	Username string
	Error    error
}

const LogContextKey contextKey = "log_context"

var httpRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests.",
	},
	[]string{"method", "path", "status"},
)

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: requestIDMiddleware(requestLogger(logger)(metricsMiddleware(mux))),
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
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("GET /debug/pprof/", s.authMiddleware(http.HandlerFunc(pprof.Index)))
	mux.Handle("GET /debug/pprof/profile", s.authMiddleware(http.HandlerFunc(pprof.Profile)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &spyResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		next.ServeHTTP(response, r)
		httpRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(response.statusCode),
		).Inc()
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = rand.Text()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			logCtx := &LogContext{}
			r = r.WithContext(context.WithValue(r.Context(), LogContextKey, logCtx))
			body := &spyReadCloser{ReadCloser: r.Body}
			r.Body = body
			response := &spyResponseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}
			next.ServeHTTP(response, r)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"client_ip", redactIP(r.RemoteAddr),
				"request_id", response.Header().Get("X-Request-ID"),
				"duration", time.Since(start),
				"request_body_bytes", body.bytesRead,
				"response_status", response.statusCode,
				"response_body_bytes", response.bytesWritten,
			}
			if logCtx.Username != "" {
				attrs = append(attrs, "user", logCtx.Username)
			}
			if logCtx.Error != nil {
				attrs = append(attrs, "error", logCtx.Error)
			}
			logger.Info(
				"Served request",
				attrs...,
			)
		})
	}
}

func redactIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return address
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return address
	}
	return fmt.Sprintf("%d.%d.%d.x", ipv4[0], ipv4[1], ipv4[2])
}

func httpError(ctx context.Context, w http.ResponseWriter, statusCode int, err error) {
	if logCtx, ok := ctx.Value(LogContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	message := err.Error()
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError:
		message = http.StatusText(statusCode)
	}
	http.Error(w, message, statusCode)
}

type responseError struct {
	message string
	cause   error
}

func (e responseError) Error() string {
	return e.message
}

func (e responseError) Unwrap() error {
	return e.cause
}

func newResponseError(message string, cause error) error {
	if cause == nil {
		return errors.New(message)
	}
	return responseError{
		message: message,
		cause:   cause,
	}
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (s *spyReadCloser) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	s.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

func (s *spyResponseWriter) WriteHeader(statusCode int) {
	s.statusCode = statusCode
	s.ResponseWriter.WriteHeader(statusCode)
}

func (s *spyResponseWriter) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	s.bytesWritten += n
	return n, err
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	addr := ln.Addr().(*net.TCPAddr)
	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", addr.Port))
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
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
