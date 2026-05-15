package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	tint "github.com/lmittmann/tint"
	isatty "github.com/mattn/go-isatty"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// var logger = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
type closeFunc func() error

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	var sensitiveKeys = []string{"password", "key", "apikey", "secret", "pin", "creditcardno", "user"}
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		if me, ok := a.Value.Any().(linkoerr.MultiError); ok {
			var errAttrs []slog.Attr
			for i, err := range me.Unwrap() {
				allAttrs := linkoerr.ErrorAttrs(err)
				errAttrs = append(errAttrs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), allAttrs...))
			}
			return slog.GroupAttrs("errors", errAttrs...)
		}
		allAttrs := linkoerr.ErrorAttrs(err)
		return slog.GroupAttrs("error", allAttrs...)
	}
	if slices.Contains(sensitiveKeys, a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	}
	if a.Value.Kind() == slog.KindString {
		valStr := a.Value.String()
		if u, err := url.Parse(valStr); err == nil && u.User != nil {
			u.User = url.User("[REDACTED]")
			return slog.String(a.Key, u.String())
		}
	}
	return a
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	// var out io.Writer = os.Stderr
	var cf closeFunc
	cf = func() error {
		return nil
	}
	fd := os.Stderr.Fd()
	isTerminal := isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
	debugHandler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor:     !isTerminal,
	})

	handlers := []slog.Handler{debugHandler}

	logFile := os.Getenv("LINKO_LOG_FILE")
	if logFile != "" {

		logger := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}
		cf = func() error {
			return logger.Close()
		}
		infoHandler := slog.NewJSONHandler(logger, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})
		handlers = append(handlers, infoHandler)
	}
	return slog.New(slog.NewMultiHandler(handlers...)), cf, nil
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
	// stdLog := log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
	// accessFile, err := os.OpenFile("linko.access.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	// if err != nil {
	// 	stdLog.Printf("failed to open access log: %v", err)
	// 	return 1
	// }
	// defer accessFile.Close()
	// accessLog := log.New(accessFile, "INFO: ", log.LstdFlags)
	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()
	logger, closeLog, err := initializeLogger()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", env),
		slog.String("hostname", hostname),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}
	defer func() {
		if err := closeLog(); err != nil {
			fmt.Fprintf(os.Stderr, "DEBUG: logger cleanup error: %v\n", err)
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error("failed to create store",
			slog.String("err_msg", err.Error()),
		)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		s.logger.Error("failed to shutdown server",
			slog.String("err_msg", err.Error()),
		)
		return 1
	}
	if serverErr != nil {
		s.logger.Error("server error",
			slog.String("err_msg", serverErr.Error()),
		)
		return 1
	}
	return 0
}
