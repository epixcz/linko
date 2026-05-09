package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
)

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

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
	logger, closeLogger, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		logger.Error(fmt.Sprintf("failed to initialize logger: %v", err))
		return 1
	}
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

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	stderrOptions := &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	}
	stderrHandler := slog.NewTextHandler(os.Stderr, stderrOptions)
	if logFile == "" {
		return slog.New(stderrHandler), func() error { return nil }, nil
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return slog.New(stderrHandler), func() error { return nil }, err
	}

	bufferedFile := bufio.NewWriterSize(f, 8192)
	closeLogger := func() error {
		if err := bufferedFile.Flush(); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}

	fileOptions := &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
	}
	fileHandler := slog.NewJSONHandler(bufferedFile, fileOptions)

	return slog.New(slog.NewMultiHandler(stderrHandler, fileHandler)), closeLogger, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		if err, ok := a.Value.Any().(error); ok {
			if stackErr, ok := errors.AsType[stackTracer](err); ok {
				return slog.GroupAttrs("error", slog.Attr{
					Key:   "message",
					Value: slog.StringValue(stackErr.Error()),
				}, slog.Attr{
					Key:   "stack_trace",
					Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
				})
			}
			return slog.String(a.Key, err.Error())
		}
	}
	return a
}
