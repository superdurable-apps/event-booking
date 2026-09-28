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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: ":" + environment("PORT", "8080"), Handler: applicationHandler(apiHandler, runtime.Webhook), ReadHeaderTimeout: 5 * time.Second}
	workerResult := runtime.StartWorker()
	if err := runtime.StartCapacity(ctx); err != nil {
		return fmt.Errorf("start event capacity Flow: %w", err)
	}
	webhookResult := runtime.StartWebhook(ctx)
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-workerResult:
		if err != nil {
			return fmt.Errorf("run Dex Worker: %w", err)
		}
	case err := <-webhookResult:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("run Stripe webhook Trigger: %w", err)
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
	mux.Handle("/api/", apiHandler)
	mux.Handle("/webhooks/stripe", stripeWebhook)
	mux.Handle("/__mock__/", http.NotFoundHandler())
	mux.Handle("/", staticHandler("web/dist"))
	return mux
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
