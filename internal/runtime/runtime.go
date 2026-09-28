package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/superdurable-apps/event-booking/internal/process"
	"github.com/superdurable/dex-connectors-library/sdkgo"
	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
)

type Runtime struct {
	Registrations *process.Service
	StripeWebhook http.Handler
	worker        *dex.Worker
	client        *dex.Client
	cache         *blobcache.Cache
	stripeTrigger sdkgo.TriggerRunner
	triggerCancel context.CancelFunc
}

func New(logger *slog.Logger) (*Runtime, error) {
	connectors, err := loadConnectors(logger)
	if err != nil {
		return nil, err
	}
	registrationFlow := process.NewRegistrationFlow(connectors.stripeConnection, connectors.gmailConnection)
	registry, err := dex.NewRegistry([]dex.Flow{process.EventInventory, registrationFlow})
	if err != nil {
		return nil, fmt.Errorf("register event Flows: %w", err)
	}
	cache, err := blobcache.New(&blobcache.Config{
		Dir:      environment("DEX_BLOB_CACHE_DIR", filepath.Join(os.TempDir(), "event-registration-blobs")),
		MaxBytes: 256 << 20, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create Dex blob cache: %w", err)
	}
	flowServiceAddress := environment("DEX_FLOW_SERVICE_ADDRESS", "127.0.0.1:8801")
	worker, err := dex.NewWorker(registry, cache, dex.WorkerOptions{
		BindAddress:        environment("DEX_WORKER_BIND_ADDRESS", "127.0.0.1:8811"),
		WorkerTarget:       dex.WorkerTarget{Address: environment("DEX_WORKER_TARGET", "127.0.0.1:8811")},
		FlowServiceAddress: flowServiceAddress, Logger: logger,
	})
	if err != nil {
		_ = cache.Close()
		return nil, fmt.Errorf("create Dex Worker: %w", err)
	}
	client, err := dex.NewClient(registry, cache, dex.ClientOptions{
		FlowServiceAddress: flowServiceAddress, WorkerTarget: worker.WorkerTarget(), Logger: logger,
	})
	if err != nil {
		_ = worker.Stop(context.Background())
		_ = cache.Close()
		return nil, fmt.Errorf("create Dex Client: %w", err)
	}
	tokenSecret := environment("EVENT_TOKEN_SECRET", "local-development-ticket-secret-change-me")
	if os.Getenv("APP_ENV") == "production" && os.Getenv("EVENT_TOKEN_SECRET") == "" {
		return nil, errors.Join(fmt.Errorf("EVENT_TOKEN_SECRET is required in production"), client.Close(), worker.Stop(context.Background()), cache.Close())
	}
	tokens, err := process.NewTokenSigner(tokenSecret)
	if err != nil {
		return nil, errors.Join(err, client.Close(), worker.Stop(context.Background()), cache.Close())
	}
	trigger, err := connectors.stripeTrigger(client, registrationFlow)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create Stripe trigger: %w", err), client.Close(), worker.Stop(context.Background()), cache.Close())
	}
	webhook, err := connectors.stripeWebhookHandler()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create Stripe webhook: %w", err), client.Close(), worker.Stop(context.Background()), cache.Close())
	}
	service := process.NewService(client, registrationFlow, tokens, environment("PUBLIC_BASE_URL", "http://127.0.0.1:8080"))
	return &Runtime{
		Registrations: service, StripeWebhook: webhook, worker: worker, client: client, cache: cache, stripeTrigger: trigger,
	}, nil
}

func (runtime *Runtime) Start() <-chan error {
	result := make(chan error, 2)
	triggerContext, cancel := context.WithCancel(context.Background())
	runtime.triggerCancel = cancel
	go func() { result <- runtime.worker.Start() }()
	go runTrigger(triggerContext, runtime.stripeTrigger, result)
	return result
}

func (runtime *Runtime) EnsureEvent(ctx context.Context) error {
	return runtime.Registrations.EnsureEvent(ctx, EventConfigFromEnvironment())
}

func (runtime *Runtime) Close() error {
	if runtime.triggerCancel != nil {
		runtime.triggerCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(runtime.worker.Stop(ctx), runtime.client.Close(), runtime.cache.Close())
}

func EventConfigFromEnvironment() process.EventConfig {
	startsAt, err := time.Parse(time.RFC3339, environment("EVENT_STARTS_AT", "2027-01-01T18:00:00-08:00"))
	if err != nil {
		startsAt = time.Date(2027, time.January, 1, 18, 0, 0, 0, time.FixedZone("PST", -8*60*60))
	}
	return process.EventConfig{
		EventID: "default", Name: environment("EVENT_NAME", "Event name to be announced"),
		Description: environment("EVENT_DESCRIPTION", "Program details will be announced soon."),
		StartsAt:    startsAt, Timezone: environment("EVENT_TIMEZONE", "America/Los_Angeles"),
		Location: environment("EVENT_LOCATION", "Location to be announced"), Currency: environment("EVENT_CURRENCY", "USD"),
		PriceMinor: positiveIntegerEnvironment("EVENT_PRICE_MINOR", 7500), Capacity: positiveIntegerEnvironment("EVENT_CAPACITY", 300),
		RegistrationOpen: environment("EVENT_REGISTRATION_OPEN", "true") == "true",
	}
}

func positiveIntegerEnvironment(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
