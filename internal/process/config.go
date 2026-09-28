package process

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

var eventIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// EventConfig contains the public event details and payment settings. Event details are
// intentionally runtime configuration so the application can ship before they are finalized.
type EventConfig struct {
	ID                          string
	Name                        string
	Description                 string
	DateTimeDisplay             string
	Location                    string
	PriceDisplay                string
	PriceCents                  int64
	Currency                    string
	Capacity                    int64
	RegistrationOpen            bool
	RegistrationDeadlineDisplay string
	OrganizerEmail              string
	PublicBaseURL               string
}

func (config EventConfig) Validate() error {
	if !eventIDPattern.MatchString(config.ID) {
		return fmt.Errorf("event ID must use lowercase letters, digits, and hyphens")
	}
	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("event name is required")
	}
	if config.Capacity < 1 || config.Capacity > 100000 {
		return fmt.Errorf("event capacity must be between 1 and 100000")
	}
	if !regexp.MustCompile(`^[a-z]{3}$`).MatchString(config.Currency) {
		return fmt.Errorf("event currency must be a lowercase three-letter code")
	}
	if config.RegistrationOpen && config.PriceCents < 1 {
		return fmt.Errorf("a positive event price is required while registration is open")
	}
	if _, err := mail.ParseAddress(config.OrganizerEmail); err != nil {
		return fmt.Errorf("organizer email is invalid: %w", err)
	}
	publicURL, err := url.Parse(config.PublicBaseURL)
	if err != nil || publicURL.Scheme == "" || publicURL.Hostname() == "" || publicURL.User != nil {
		return fmt.Errorf("public base URL must be absolute and contain no credentials")
	}
	if publicURL.Scheme != "https" && publicURL.Hostname() != "localhost" && publicURL.Hostname() != "127.0.0.1" {
		return fmt.Errorf("public base URL must use HTTPS")
	}
	return nil
}

func (config EventConfig) CapacityFlowID() string { return "event-capacity-" + config.ID }
