package apphost

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/superdurable-apps/event-booking/internal/connectorconfiguration"
	"github.com/superdurable-apps/event-booking/internal/process"
	gmail "github.com/superdurable/dex-connectors-library/connectors/google/gmail"
	stripe "github.com/superdurable/dex-connectors-library/connectors/stripe"
	"github.com/superdurable/dex-connectors-library/sdkgo"
	"github.com/superdurable/dex-connectors-library/sdkgo/hostedconfig"
	"github.com/superdurable/dex-connectors-library/sdkgo/localconfig"
	"github.com/superdurable/dex/sdk-go/dex"
)

type connectorBundle struct {
	stripeConnection stripe.Connection
	gmailConnection  gmail.Connection
	store            *localconfig.Store
	hosted           *connectorconfiguration.Configuration
	logger           *slog.Logger
}

func loadConnectors(logger *slog.Logger) (*connectorBundle, error) {
	if strings.TrimSpace(os.Getenv("SUPERVERSE_CONNECTOR_CONFIG_FILE")) != "" {
		return loadHostedConnectors(logger)
	}
	if path := strings.TrimSpace(os.Getenv(localconfig.EnvironmentVariable)); path != "" {
		store, err := localconfig.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load connector configuration: %w", err)
		}
		stripeConnection, err := stripe.NewLocalConnection(store, process.StripeConnectionName)
		if err != nil {
			return nil, fmt.Errorf("load Stripe connection: %w", err)
		}
		gmailConnection, err := gmail.NewLocalConnection(store, process.GmailConnectionName, gmail.WithLogger(logger))
		if err != nil {
			return nil, fmt.Errorf("load Gmail connection: %w", err)
		}
		return &connectorBundle{stripeConnection: stripeConnection, gmailConnection: gmailConnection, store: store, logger: logger}, nil
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
		return nil, fmt.Errorf("%s is required in production", localconfig.EnvironmentVariable)
	}
	stripeReference := sdkgo.ConnectionRef{Provider: "stripe", Name: process.StripeConnectionName}
	stripeConfig := stripe.DefaultConfig()
	stripeConfig.Endpoint = environment("STRIPE_ENDPOINT", "http://127.0.0.1:9")
	stripeClient, err := stripe.New(stripeConfig, sdkgo.StaticCredentialProvider[stripe.Credentials]{
		stripeReference: {
			SecretKey:     sdkgo.NewSecretString(environment("STRIPE_SECRET_KEY", "sk_test_local_unconfigured")),
			WebhookSecret: sdkgo.NewSecretString(environment("STRIPE_WEBHOOK_SECRET", "whsec_local_development")),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create development Stripe connector: %w", err)
	}
	stripeConnection, err := stripe.NewConnection(stripeClient, stripeReference)
	if err != nil {
		return nil, err
	}
	gmailReference := sdkgo.ConnectionRef{Provider: "google", Name: process.GmailConnectionName}
	gmailConfig := gmail.DefaultConfig()
	gmailConfig.Endpoint = environment("GMAIL_ENDPOINT", "http://127.0.0.1:9")
	gmailClient, err := gmail.New(gmailConfig, sdkgo.StaticCredentialProvider[gmail.Credentials]{
		gmailReference: {
			AccessToken:  sdkgo.NewSecretString(environment("GMAIL_ACCESS_TOKEN", "local-unconfigured")),
			PrimaryEmail: environment("GMAIL_PRIMARY_EMAIL", "tickets@example.com"),
		},
	}, gmail.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("create development Gmail connector: %w", err)
	}
	gmailConnection, err := gmail.NewConnection(gmailClient, gmailReference)
	if err != nil {
		return nil, err
	}
	logger.Warn("connector configuration is absent; provider calls use non-routable development endpoints")
	return &connectorBundle{stripeConnection: stripeConnection, gmailConnection: gmailConnection, logger: logger}, nil
}

func loadHostedConnectors(logger *slog.Logger) (*connectorBundle, error) {
	configuration, err := connectorconfiguration.Load(true)
	if err != nil {
		return nil, fmt.Errorf("load hosted connector configuration: %w", err)
	}
	stripeConfiguration := stripe.DefaultConfig()
	if err := configuration.DecodeConnectionConfiguration(
		stripe.ConnectorID, process.StripeConnectionName, &stripeConfiguration,
	); err != nil {
		return nil, fmt.Errorf("load hosted Stripe configuration: %w", err)
	}
	stripeCredentials, err := hostedconfig.NewCredentialProviderFromEnvironment(
		stripe.ConnectorID, process.StripeConnectionName, stripe.DecodeResolvedCredentialsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("create hosted Stripe credential provider: %w", err)
	}
	stripeClient, err := stripe.New(stripeConfiguration, stripeCredentials)
	if err != nil {
		return nil, fmt.Errorf("create hosted Stripe connector: %w", err)
	}
	stripeConnection, err := stripe.NewConnection(
		stripeClient,
		sdkgo.ConnectionRef{Provider: "stripe", Name: process.StripeConnectionName},
	)
	if err != nil {
		return nil, fmt.Errorf("create hosted Stripe connection: %w", err)
	}

	gmailConfiguration := gmail.DefaultConfig()
	if err := configuration.DecodeConnectionConfiguration(
		gmail.ConnectorID, process.GmailConnectionName, &gmailConfiguration,
	); err != nil {
		return nil, fmt.Errorf("load hosted Gmail configuration: %w", err)
	}
	gmailCredentials, err := hostedconfig.NewCredentialProviderFromEnvironment(
		gmail.ConnectorID, process.GmailConnectionName, gmail.DecodeResolvedCredentialsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("create hosted Gmail credential provider: %w", err)
	}
	gmailClient, err := gmail.New(gmailConfiguration, gmailCredentials, gmail.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("create hosted Gmail connector: %w", err)
	}
	gmailConnection, err := gmail.NewConnection(
		gmailClient,
		sdkgo.ConnectionRef{Provider: "google", Name: process.GmailConnectionName},
	)
	if err != nil {
		return nil, fmt.Errorf("create hosted Gmail connection: %w", err)
	}
	return &connectorBundle{
		stripeConnection: stripeConnection,
		gmailConnection:  gmailConnection,
		hosted:           &configuration,
		logger:           logger,
	}, nil
}

func (bundle *connectorBundle) stripeWebhookHandler() (http.Handler, error) {
	return bundle.stripeConnection.CheckoutSessionWebhookHandler()
}

func (bundle *connectorBundle) stripeTrigger(client *dex.Client, flow *process.RegistrationFlow) (sdkgo.TriggerRunner, error) {
	registrationTarget := sdkgo.NewDexRPCTriggerTarget(
		client,
		flow.ReceiveStripeEvent,
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) bool {
			return strings.HasPrefix(event.Payload.Session.ClientReferenceID, "registration-")
		},
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) string {
			return event.Payload.Session.ClientReferenceID
		},
		func(event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) process.PaymentEvent {
			return process.PaymentEvent{EventID: event.ID, Type: event.Payload.Type, Session: event.Payload.Session, ObservedAt: time.Now().UTC()}
		},
		sdkgo.WithTriggerLogger(bundle.logger),
	)
	var target sdkgo.TriggerTarget[stripe.CheckoutSessionEvent] = sdkgo.TriggerTargetFunc[stripe.CheckoutSessionEvent](func(ctx context.Context, event sdkgo.TriggerEvent[stripe.CheckoutSessionEvent]) error {
		registrationID := event.Payload.Session.ClientReferenceID
		if !strings.HasPrefix(registrationID, "registration-") {
			return nil
		}
		var state process.RegistrationState
		if err := client.InvokeRPC(ctx, registrationID, flow.DescribeRegistration, nil, &state); err != nil {
			return err
		}
		if state.CheckoutSessionID != "" && state.CheckoutSessionID != event.Payload.Session.ID {
			return sdkgo.MarkTriggerUndeliverable(fmt.Errorf("Stripe Checkout Session does not match registration"))
		}
		if event.Payload.Type == "checkout.session.async_payment_succeeded" || (event.Payload.Type == "checkout.session.completed" && event.Payload.Session.PaymentStatus == "paid") {
			if event.Payload.Session.PaymentStatus != "paid" || event.Payload.Session.AmountTotal != state.Event.PriceMinor || !strings.EqualFold(event.Payload.Session.Currency, state.Event.Currency) {
				return sdkgo.MarkTriggerUndeliverable(fmt.Errorf("Stripe paid amount or currency does not match registration"))
			}
			var inventory process.EventView
			if err := client.InvokeRPC(ctx, process.EventFlowID, process.EventInventory.MarkSeatPaid, process.RegistrationReference{
				RegistrationID: registrationID, UpdatedAt: event.OccurredAt,
			}, &inventory); err != nil {
				return err
			}
		}
		if event.Payload.Type == "checkout.session.async_payment_failed" || event.Payload.Type == "checkout.session.expired" {
			var inventory process.EventView
			if err := client.InvokeRPC(ctx, process.EventFlowID, process.EventInventory.ReleaseSeat, process.RegistrationReference{
				RegistrationID: registrationID, UpdatedAt: event.OccurredAt,
			}, &inventory); err != nil {
				return err
			}
		}
		return registrationTarget.HandleTrigger(ctx, event)
	})
	configuration := stripe.CheckoutSessionUpdatedTriggerConfiguration{}
	if bundle.store != nil {
		if err := bundle.store.DecodeTriggerConfiguration(
			stripe.ConnectorID, process.StripeConnectionName, "checkoutSessionUpdated", process.StripeTriggerBindingName, &configuration,
		); err != nil {
			return nil, err
		}
		durableTarget, err := localconfig.NewDurableTriggerTarget(
			bundle.store, stripe.ConnectorID, process.StripeConnectionName, "checkoutSessionUpdated", process.StripeTriggerBindingName, target,
		)
		if err != nil {
			return nil, err
		}
		target = durableTarget
	} else if bundle.hosted != nil {
		if err := bundle.hosted.DecodeTriggerConfiguration(
			stripe.ConnectorID,
			process.StripeConnectionName,
			"checkoutSessionUpdated",
			process.StripeTriggerBindingName,
			&configuration,
		); err != nil {
			return nil, err
		}
	}
	return stripe.NewCheckoutSessionUpdatedTrigger(stripe.CheckoutSessionUpdatedTriggerConfig{
		Connection: bundle.stripeConnection, ConnectionName: process.StripeConnectionName, BindingName: process.StripeTriggerBindingName,
		Configuration: configuration, Target: target,
	}), nil
}

func runTrigger(ctx context.Context, trigger sdkgo.TriggerRunner, result chan<- error) {
	result <- trigger.Run(ctx)
}
