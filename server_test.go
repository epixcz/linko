package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_requestLogger(t *testing.T) {
	logBuffer, loggedHandler := testRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, httptest.NewRequest("GET", "http://lin.ko/api/stats", nil))

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.1:1234 duration=1s request_body_bytes=0 response_status=200 response_body_bytes=0` + "\n"
	const expectedStatusCode = http.StatusOK

	if logBuffer.String() != expectedLogString {
		t.Errorf("expected log string %q, got %q", expectedLogString, logBuffer.String())
	}
	if rr.Code != expectedStatusCode {
		t.Errorf("expected status code %d, got %d", expectedStatusCode, rr.Code)
	}
}

func Test_requestLoggerIncludesAuthenticatedUser(t *testing.T) {
	logBuffer, loggedHandler := testRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logCtx := r.Context().Value(LogContextKey).(*LogContext)
		logCtx.Username = "frodo"
	}))

	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, httptest.NewRequest("GET", "http://lin.ko/api/stats", nil))

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.1:1234 duration=1s request_body_bytes=0 response_status=200 response_body_bytes=0 user=frodo` + "\n"
	if logBuffer.String() != expectedLogString {
		t.Errorf("expected log string %q, got %q", expectedLogString, logBuffer.String())
	}
}

func Test_requestLoggerIncludesResponseError(t *testing.T) {
	logBuffer, loggedHandler := testRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpError(r.Context(), w, http.StatusTeapot, errors.New("teapot unavailable"))
	}))

	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, httptest.NewRequest("GET", "http://lin.ko/api/stats", nil))

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.1:1234 duration=1s request_body_bytes=0 response_status=418 response_body_bytes=19 error.message="teapot unavailable"` + "\n"
	if logBuffer.String() != expectedLogString {
		t.Errorf("expected log string %q, got %q", expectedLogString, logBuffer.String())
	}
}

func testRequestLogger(next http.Handler) (*bytes.Buffer, http.Handler) {
	logBuffer := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			if a.Key == "duration" {
				return slog.Duration("duration", time.Second)
			}
			return replaceAttr(groups, a)
		},
	}))
	return logBuffer, requestLogger(logger)(next)
}
