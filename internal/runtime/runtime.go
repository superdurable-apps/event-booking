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
	"strings"
	"time"

	"github.com/superdurable-apps/event-booking/internal/process"
	gmail "github.com/superdurable/dex-connectors-library/connectors/google/gmail"
	"github.com/superdurable/dex-connectors-library/connectors/stripe"
	"github.com/superdurable/dex-connectors-library/sdkgo"
	"github.com/superdurable/dex-connectors-library/sdkgo/localconfig"
	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
)

type Runtime struct {
	Registrations *process.Service
	Webhook       http.Handler
	capacity      *process.CapacityClient
	webhook       *stripe.CheckoutSessionWebhookRuntime
	worker        *dex.Worker
	client        *dex.Client
	cache         *blobcache.Cache
}

func New(logger *slog.Logger) (*Runtime, error) {
	config, err := eventConfigFromEnvironment()
	if err != nil {
		return nil, err
	}
	tokens, err := process.NewTokenSigner(os.Getenv("TICKET_SIGNING_KEY"), config.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	store, err := localconfig.LoadFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("load connector configuration: %w", err)
	}
	stripeConnection, err := stripe.NewLocalConnection(store, process.StripeConnectionName)
	if err != nil {
		return nil, fmt.Errorf("load Stripe connection: %w", err)
	}
	gmailConnection, err := gmail.NewLocalConnection(store, process.GmailConnectionName)
	if err != nil {
		return nil, fmt.Errorf("load Gmail connection: %w", err)
	}
	capacity := process.NewCapacityClient(config)
	registrationFlow := process.NewRegistrationFlow(config, capacity, stripeConnection, gmailConnection, tokens)
	registry, err := dex.NewRegistry([]dex.Flow{process.EventCapacityFlow, registrationFlow})
	if err != nil {
		return nil, fmt.Errorf("register event Flows: %w", err)
	}
	cache, err := blobcache.New(&blobcache.Config{
		Dir: environment("DEX_BLOB_CACHE_DIR", filepath.Join(os.TempDir(), "event-booking-blobs")), MaxBytes: 256 << 20, Logger: logger,
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
	capacity.Attach(client)
	service, err := process.NewService(client, registrationFlow, capacity, config, tokens, os.Getenv("STAFF_CHECKIN_TOKEN"))
	if err != nil {
		return nil, errors.Join(err, client.Close(), stopWorker(worker), cache.Close())
	}
	target := sdkgo.NewDexRPCTriggerTarget(
		client, registrationFlow.ApplyStripeEvent,
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) bool {
			return event.Payload.Session.ClientReferenceID != "" && event.Payload.Session.Metadata["event_id"] == config.ID
		},
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) string {
			return "registration-" + event.Payload.Session.ClientReferenceID
		},
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) process.ApplyStripeEventInput {
			return process.ApplyStripeEventInput{EventID: event.ID, OccurredAt: event.OccurredAt, Event: event.Payload}
		},
		sdkgo.WithTriggerLogger(logger.With("connector", stripe.ConnectorID, "binding", process.StripeTriggerBinding)),
	)
	webhook, err := stripe.NewLocalCheckoutSessionWebhookRuntime(store, process.StripeConnectionName,
		[]stripe.LocalCheckoutSessionUpdatedTriggerRoute{{BindingName: process.StripeTriggerBinding, Target: target}})
	if err != nil {
		return nil, errors.Join(err, client.Close(), stopWorker(worker), cache.Close())
	}
	return &Runtime{
		Registrations: service, Webhook: webhook, capacity: capacity, webhook: webhook,
		worker: worker, client: client, cache: cache,
	}, nil
}

func (runtime *Runtime) StartWorker() <-chan error {
	result := make(chan error, 1)
	go func() { result <- runtime.worker.Start() }()
	return result
}

func (runtime *Runtime) StartWebhook(ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() { result <- runtime.webhook.Run(ctx) }()
	return result
}

func (runtime *Runtime) StartCapacity(ctx context.Context) error { return runtime.capacity.Start(ctx) }

func (runtime *Runtime) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(runtime.worker.Stop(ctx), runtime.client.Close(), runtime.cache.Close())
}

func eventConfigFromEnvironment() (process.EventConfig, error) {
	capacity, err := int64Environment("EVENT_CAPACITY", 300)
	if err != nil {
		return process.EventConfig{}, err
	}
	priceCents, err := int64Environment("EVENT_PRICE_CENTS", 0)
	if err != nil {
		return process.EventConfig{}, err
	}
	registrationOpen, err := boolEnvironment("REGISTRATION_OPEN", false)
	if err != nil {
		return process.EventConfig{}, err
	}
	config := process.EventConfig{
		ID: environment("EVENT_ID", "event-tbd"), Name: environment("EVENT_NAME", "Event details coming soon"),
		Description:     environment("EVENT_DESCRIPTION", "Event details will be announced soon."),
		DateTimeDisplay: environment("EVENT_DATE_DISPLAY", "To be announced"), Location: environment("EVENT_LOCATION", "To be announced"),
		PriceDisplay: environment("EVENT_PRICE_DISPLAY", "To be announced"), PriceCents: priceCents,
		Currency: environment("EVENT_CURRENCY", "usd"), Capacity: capacity, RegistrationOpen: registrationOpen,
		RegistrationDeadlineDisplay: environment("REGISTRATION_DEADLINE_DISPLAY", "To be announced"),
		OrganizerEmail:              environment("ORGANIZER_EMAIL", "events@example.com"), PublicBaseURL: environment("PUBLIC_BASE_URL", "http://localhost:8080"),
	}
	if err := config.Validate(); err != nil {
		return process.EventConfig{}, err
	}
	return config, nil
}

func int64Environment(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func boolEnvironment(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func stopWorker(worker *dex.Worker) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return worker.Stop(ctx)
}
