package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

// var logger = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
type closeFunc func() error

func initializeLogger() (*slog.Logger, closeFunc, error) {
	// var out io.Writer = os.Stderr
	var cf closeFunc
	cf = func() error {
		return nil
	}
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	handlers := []slog.Handler{debugHandler}

	logFile := os.Getenv("LINKO_LOG_FILE")
	if logFile != "" {

		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, cf, fmt.Errorf("failed to open log file: %w", err)
		} else {
			bufWriter := bufio.NewWriterSize(f, 8192)
			cf = func() error {
				if err := bufWriter.Flush(); err != nil {
					f.Close()
					return err
				}
				return f.Close()
			}
			infoHandler := slog.NewJSONHandler(bufWriter, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			})
			handlers = append(handlers, infoHandler)
		}
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
	logger, closeLog, err := initializeLogger()
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
