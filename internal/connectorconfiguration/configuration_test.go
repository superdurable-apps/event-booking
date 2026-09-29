// Copyright (c) 2026 Super Durable
// SPDX-License-Identifier: MIT

package connectorconfiguration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllowsAnEmptyLocalConnectorSet(t *testing.T) {
	clearConfigurationEnvironment(t)

	configuration, err := Load(false)
	if err != nil {
		t.Fatalf("load empty local connector configuration: %v", err)
	}
	if configuration.Mode != ModeLocal || configuration.ConfigurationFile != "" {
		t.Fatalf("unexpected local configuration: %+v", configuration)
	}
}

func TestLoadValidatesAHostedSnapshotAndCredentialBoundary(t *testing.T) {
	clearConfigurationEnvironment(t)
	directory := t.TempDir()
	configurationFile := filepath.Join(directory, "connections.json")
	credentialFile := filepath.Join(directory, "workload-token")
	contents := []byte(`{"connections":[]}`)
	if err := os.WriteFile(configurationFile, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.WriteFile(credentialFile, []byte("opaque-token"), 0o600); err != nil {
		t.Fatalf("write workload credential: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", hex.EncodeToString(digest[:]))
	t.Setenv("SUPERVERSE_CONNECTOR_BROKER_URL", "https://connector-broker.internal")
	t.Setenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE", credentialFile)
	t.Setenv("PUBLIC_BASE_URL", "https://application.example.test/")

	configuration, err := Load(true)
	if err != nil {
		t.Fatalf("load hosted connector configuration: %v", err)
	}
	if configuration.Mode != ModeHosted || configuration.ConfigurationFile != configurationFile {
		t.Fatalf("unexpected hosted configuration: %+v", configuration)
	}
	if configuration.PublicBaseURL != "https://application.example.test" {
		t.Fatalf("unexpected public base URL: %q", configuration.PublicBaseURL)
	}
}

func TestLoadAllowsAnInternalKubernetesCredentialBroker(t *testing.T) {
	clearConfigurationEnvironment(t)
	directory := t.TempDir()
	configurationFile := filepath.Join(directory, "connections.json")
	credentialFile := filepath.Join(directory, "workload-token")
	contents := []byte(`{"connections":[]}`)
	if err := os.WriteFile(configurationFile, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.WriteFile(credentialFile, []byte("opaque-token"), 0o600); err != nil {
		t.Fatalf("write workload credential: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", hex.EncodeToString(digest[:]))
	t.Setenv("SUPERVERSE_CONNECTOR_BROKER_URL", "http://connector-broker.superverse-system.svc.cluster.local:8091/")
	t.Setenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE", credentialFile)
	t.Setenv("PUBLIC_BASE_URL", "http://staging-project.local.invalid")

	if _, err := Load(true); err != nil {
		t.Fatalf("load hosted configuration with an internal broker: %v", err)
	}
}

func TestLoadRejectsAPlaintextExternalCredentialBroker(t *testing.T) {
	clearConfigurationEnvironment(t)
	directory := t.TempDir()
	configurationFile := filepath.Join(directory, "connections.json")
	credentialFile := filepath.Join(directory, "workload-token")
	contents := []byte(`{"connections":[]}`)
	if err := os.WriteFile(configurationFile, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.WriteFile(credentialFile, []byte("opaque-token"), 0o600); err != nil {
		t.Fatalf("write workload credential: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", hex.EncodeToString(digest[:]))
	t.Setenv("SUPERVERSE_CONNECTOR_BROKER_URL", "http://connector-broker.example.test:8091/")
	t.Setenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE", credentialFile)
	t.Setenv("PUBLIC_BASE_URL", "https://application.example.test")

	if _, err := Load(true); err == nil || err.Error() != "SUPERVERSE_CONNECTOR_BROKER_URL must use HTTPS or an internal Kubernetes service URL" {
		t.Fatalf("expected plaintext external broker rejection, got %v", err)
	}
}

func TestLoadFailsClosedWhenTheHostedDigestDoesNotMatch(t *testing.T) {
	clearConfigurationEnvironment(t)
	configurationFile := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(configurationFile, []byte(`{"connections":[]}`), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", "0"+"123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("PUBLIC_BASE_URL", "https://application.example.test")

	if _, err := Load(false); err == nil || err.Error() != "hosted connector configuration digest does not match" {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestLoadRejectsAWorldReadableWorkloadCredential(t *testing.T) {
	clearConfigurationEnvironment(t)
	directory := t.TempDir()
	configurationFile := filepath.Join(directory, "connections.json")
	credentialFile := filepath.Join(directory, "workload-token")
	contents := []byte(`{"connections":[]}`)
	if err := os.WriteFile(configurationFile, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.WriteFile(credentialFile, []byte("opaque-token"), 0o644); err != nil {
		t.Fatalf("write workload credential: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", hex.EncodeToString(digest[:]))
	t.Setenv("SUPERVERSE_CONNECTOR_BROKER_URL", "https://connector-broker.internal")
	t.Setenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE", credentialFile)
	t.Setenv("PUBLIC_BASE_URL", "https://application.example.test")

	if _, err := Load(true); err == nil {
		t.Fatal("expected insecure workload credential permissions to fail")
	}
}

func TestHostedConfigurationDecodesConnectionAndTriggerValues(t *testing.T) {
	clearConfigurationEnvironment(t)
	directory := t.TempDir()
	configurationFile := filepath.Join(directory, "connections.json")
	credentialFile := filepath.Join(directory, "workload-token")
	contents := []byte(`{
		"configurationRevision":"revision-1",
		"configurationState":"READY",
		"connections":[{
			"connectorId":"stripe",
			"connectionName":"event-payments",
			"configuration":{"endpoint":"https://stripe.example.test"}
		}],
		"triggerBindings":[{
			"connectorId":"stripe",
			"connectionName":"event-payments",
			"triggerName":"checkoutSessionUpdated",
			"bindingName":"event-registration-payments",
			"configuration":{"eventTypes":["checkout.session.completed"]}
		}],
		"operationConfigurations":[]
	}`)
	if err := os.WriteFile(configurationFile, contents, 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.WriteFile(credentialFile, []byte("opaque-token"), 0o600); err != nil {
		t.Fatalf("write workload credential: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_FILE", configurationFile)
	t.Setenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST", hex.EncodeToString(digest[:]))
	t.Setenv("SUPERVERSE_CONNECTOR_BROKER_URL", "https://connector-broker.internal")
	t.Setenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE", credentialFile)
	t.Setenv("PUBLIC_BASE_URL", "https://application.example.test")

	configuration, err := Load(true)
	if err != nil {
		t.Fatalf("load hosted connector configuration: %v", err)
	}
	var connection struct {
		Endpoint string `json:"endpoint"`
	}
	if err := configuration.DecodeConnectionConfiguration(
		"stripe", "event-payments", &connection,
	); err != nil {
		t.Fatalf("decode connection configuration: %v", err)
	}
	if connection.Endpoint != "https://stripe.example.test" {
		t.Fatalf("unexpected endpoint: %q", connection.Endpoint)
	}
	var trigger struct {
		EventTypes []string `json:"eventTypes"`
	}
	if err := configuration.DecodeTriggerConfiguration(
		"stripe",
		"event-payments",
		"checkoutSessionUpdated",
		"event-registration-payments",
		&trigger,
	); err != nil {
		t.Fatalf("decode Trigger configuration: %v", err)
	}
	if len(trigger.EventTypes) != 1 || trigger.EventTypes[0] != "checkout.session.completed" {
		t.Fatalf("unexpected Trigger configuration: %+v", trigger)
	}
}

func clearConfigurationEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DEX_CONNECTOR_CONFIG_FILE",
		"SUPERVERSE_CONNECTOR_CONFIG_FILE",
		"SUPERVERSE_CONNECTOR_CONFIG_DIGEST",
		"SUPERVERSE_CONNECTOR_BROKER_URL",
		"SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE",
		"PUBLIC_BASE_URL",
	} {
		t.Setenv(name, "")
	}
}
