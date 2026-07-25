package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
}

var (
	sensitiveLogKeys = []string{"password", "key", "apikey", "secret", "pin", "creditcardno", "user"}
	urlPattern       = regexp.MustCompile(`https?://[^\s"']+`)
	tracer           trace.Tracer
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)

	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	shutdownTracing, err := initTracing(ctx)
	if err != nil {
		os.Stderr.WriteString("failed to initialize tracing: " + err.Error() + "\n")
		return 1
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			os.Stderr.WriteString("failed to shut down tracing: " + err.Error() + "\n")
		}
	}()

	logger, closeLogger, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		logger.Error(fmt.Sprintf("failed to initialize logger: %v", err))
		return 1
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)
	defer func() {
		if err := closeLogger(); err != nil {
			os.Stderr.WriteString("failed to clean up logger: " + err.Error() + "\n")
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create store: %v", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	logger.Debug("Linko is shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}

func initTracing(ctx context.Context) (func(context.Context) error, error) {
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(resource.Default()),
	)

	otel.SetTracerProvider(tp)
	tracer = otel.Tracer("boot.dev/linko")
	return tp.Shutdown, nil
}

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	stderrFD := os.Stderr.Fd()
	useColor := isatty.IsTerminal(stderrFD) || isatty.IsCygwinTerminal(stderrFD)
	stderrOptions := &tint.Options{
		Level:       slog.LevelDebug,
		NoColor:     !useColor,
		ReplaceAttr: replaceAttr,
	}
	stderrHandler := tint.NewHandler(os.Stderr, stderrOptions)
	if logFile == "" {
		return slog.New(stderrHandler), func() error { return nil }, nil
	}

	fileLogger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    1,
		MaxAge:     28,
		MaxBackups: 10,
		LocalTime:  false,
		Compress:   true,
	}

	fileOptions := &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
	}
	fileHandler := slog.NewJSONHandler(fileLogger, fileOptions)

	return slog.New(slog.NewMultiHandler(stderrHandler, fileHandler)), fileLogger.Close, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	a = redactAttr(a)
	if a.Key == "error" {
		if err, ok := a.Value.Any().(error); ok {
			if multiErr, ok := errors.AsType[multiError](err); ok {
				var attrs []slog.Attr
				for i, err := range multiErr.Unwrap() {
					attrs = append(attrs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errorAttrs(err)...))
				}
				return slog.GroupAttrs("errors", attrs...)
			}
			return slog.GroupAttrs("error", errorAttrs(err)...)
		}
	}
	return a
}

func redactAttr(a slog.Attr) slog.Attr {
	if slices.Contains(sensitiveLogKeys, strings.ToLower(a.Key)) {
		a.Value = slog.StringValue("[REDACTED]")
		return a
	}
	if a.Value.Kind() == slog.KindString {
		a.Value = slog.StringValue(redactURLPasswords(a.Value.String()))
	}
	return a
}

func redactAttrs(attrs []slog.Attr) []slog.Attr {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, redactAttr(attr))
	}
	return redacted
}

func redactURLPasswords(value string) string {
	if redacted := redactURLPassword(value); redacted != value {
		return redacted
	}
	return urlPattern.ReplaceAllStringFunc(value, redactURLPassword)
}

func redactURLPassword(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User == nil {
		return value
	}
	if _, ok := parsed.User.Password(); !ok {
		return value
	}
	withoutUser := *parsed
	withoutUser.User = nil
	prefix := withoutUser.Scheme + "://"
	withoutUserString := withoutUser.String()
	if !strings.HasPrefix(withoutUserString, prefix) {
		return value
	}
	return prefix + parsed.User.Username() + ":[REDACTED]@" + strings.TrimPrefix(withoutUserString, prefix)
}

func errorAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{{
		Key:   "message",
		Value: slog.StringValue(redactURLPasswords(err.Error())),
	}}
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		})
	}
	attrs = append(attrs, redactAttrs(linkoerr.Attrs(err))...)
	return attrs
}
