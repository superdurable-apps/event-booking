package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	api "github.com/superdurable-apps/event-booking/internal/api"
	appRuntime "github.com/superdurable-apps/event-booking/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	runtime, err := appRuntime.New(logger)
	if err != nil {
		return err
	}
	defer runtime.Close()
	apiHandler, err := api.NewHandler(runtime.Registrations)
	if err != nil {
		return fmt.Errorf("create OpenAPI handler: %w", err)
	}
	server := &http.Server{Addr: ":" + environment("PORT", "8080"), Handler: applicationHandler(apiHandler, runtime.StripeWebhook), ReadHeaderTimeout: 5 * time.Second}
	runtimeResult := runtime.Start()
	initializeContext, initializeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer initializeCancel()
	if err := ensureEvent(initializeContext, runtime.EnsureEvent); err != nil {
		return fmt.Errorf("initialize event inventory: %w", err)
	}
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.ListenAndServe() }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-runtimeResult:
		if err != nil {
			return fmt.Errorf("run Dex runtime: %w", err)
		}
	case err := <-serverResult:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("run HTTP server: %w", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

func applicationHandler(apiHandler http.Handler, stripeWebhook http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/connectors/stripe/webhook", stripeWebhook)
	mux.Handle("/api/", apiHandler)
	mux.Handle("/", staticHandler("web/dist"))
	return mux
}

func ensureEvent(ctx context.Context, initialize func(context.Context) error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = initialize(attempt)
		cancel()
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func staticHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		asset := filepath.Join(root, filepath.Clean(request.URL.Path))
		if request.URL.Path != "/" {
			if info, err := os.Stat(asset); err == nil && !info.IsDir() {
				files.ServeHTTP(w, request)
				return
			}
		}
		http.ServeFile(w, request, filepath.Join(root, "index.html"))
	})
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
