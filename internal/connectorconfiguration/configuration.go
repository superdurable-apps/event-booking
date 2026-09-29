// Copyright (c) 2026 Super Durable
// SPDX-License-Identifier: MIT

// Package connectorconfiguration validates the platform-owned connector snapshot boundary.
package connectorconfiguration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maximumConfigurationBytes = 32 << 20
	maximumCredentialBytes    = 16 << 10
)

// Mode identifies the active connector configuration authority.
type Mode string

const (
	// ModeLocal reads the developer-owned Dex connector configuration file.
	ModeLocal Mode = "local"
	// ModeHosted reads a deployment-pinned snapshot and resolves credentials through the broker.
	ModeHosted Mode = "hosted"
)

// Configuration describes safe connector inputs without loading credential material.
type Configuration struct {
	Mode                   Mode
	ConfigurationFile      string
	CredentialBrokerURL    string
	WorkloadCredentialFile string
	PublicBaseURL          string
	snapshot               []byte
}

type hostedSnapshot struct {
	ConfigurationRevision   string            `json:"configurationRevision"`
	ConfigurationState      string            `json:"configurationState"`
	Connections             []json.RawMessage `json:"connections"`
	TriggerBindings         []json.RawMessage `json:"triggerBindings"`
	OperationConfigurations []json.RawMessage `json:"operationConfigurations"`
}

type hostedConnection struct {
	ConnectorID    string          `json:"connectorId"`
	ConnectionName string          `json:"connectionName"`
	Configuration  json.RawMessage `json:"configuration"`
}

type hostedTriggerBinding struct {
	ConnectorID    string          `json:"connectorId"`
	ConnectionName string          `json:"connectionName"`
	TriggerName    string          `json:"triggerName"`
	BindingName    string          `json:"bindingName"`
	Configuration  json.RawMessage `json:"configuration"`
}

// Load validates local or hosted connector configuration before the Worker starts.
func Load(requiresConnections bool) (Configuration, error) {
	hostedFile := strings.TrimSpace(os.Getenv("SUPERVERSE_CONNECTOR_CONFIG_FILE"))
	localFile := strings.TrimSpace(os.Getenv("DEX_CONNECTOR_CONFIG_FILE"))
	if hostedFile != "" || hasHostedEnvironment() {
		if localFile != "" {
			return Configuration{}, fmt.Errorf("local and hosted connector configuration cannot be combined")
		}
		return loadHosted(hostedFile, requiresConnections)
	}
	if localFile == "" {
		if requiresConnections {
			return Configuration{}, fmt.Errorf("DEX_CONNECTOR_CONFIG_FILE is required")
		}
		return Configuration{Mode: ModeLocal}, nil
	}
	if _, err := readBoundedRegularFile(localFile, maximumConfigurationBytes, false); err != nil {
		return Configuration{}, fmt.Errorf("read local connector configuration: %w", err)
	}
	return Configuration{Mode: ModeLocal, ConfigurationFile: localFile}, nil
}

func loadHosted(configurationFile string, requiresConnections bool) (Configuration, error) {
	if configurationFile == "" || !filepath.IsAbs(configurationFile) {
		return Configuration{}, fmt.Errorf("SUPERVERSE_CONNECTOR_CONFIG_FILE must be an absolute path")
	}
	contents, err := readBoundedRegularFile(configurationFile, maximumConfigurationBytes, false)
	if err != nil {
		return Configuration{}, fmt.Errorf("read hosted connector configuration: %w", err)
	}
	expectedDigest := strings.TrimSpace(os.Getenv("SUPERVERSE_CONNECTOR_CONFIG_DIGEST"))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(expectedDigest) {
		return Configuration{}, fmt.Errorf("SUPERVERSE_CONNECTOR_CONFIG_DIGEST must be lowercase hexadecimal")
	}
	actualDigest := sha256.Sum256(contents)
	if expectedDigest != hex.EncodeToString(actualDigest[:]) {
		return Configuration{}, fmt.Errorf("hosted connector configuration digest does not match")
	}
	publicBaseURL, err := validatedHTTPURL("PUBLIC_BASE_URL", true)
	if err != nil {
		return Configuration{}, err
	}
	brokerURL := strings.TrimSpace(os.Getenv("SUPERVERSE_CONNECTOR_BROKER_URL"))
	credentialFile := strings.TrimSpace(os.Getenv("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE"))
	if brokerURL == "" && requiresConnections {
		return Configuration{}, fmt.Errorf("SUPERVERSE_CONNECTOR_BROKER_URL is required")
	}
	if brokerURL != "" {
		if _, err := validatedHTTPURL("SUPERVERSE_CONNECTOR_BROKER_URL", false); err != nil {
			return Configuration{}, err
		}
	}
	if credentialFile == "" && requiresConnections {
		return Configuration{}, fmt.Errorf("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE is required")
	}
	if credentialFile != "" {
		if !filepath.IsAbs(credentialFile) {
			return Configuration{}, fmt.Errorf("SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE must be an absolute path")
		}
		if _, err := readBoundedRegularFile(credentialFile, maximumCredentialBytes, true); err != nil {
			return Configuration{}, fmt.Errorf("read workload credential: %w", err)
		}
	}
	return Configuration{
		Mode:                   ModeHosted,
		ConfigurationFile:      configurationFile,
		CredentialBrokerURL:    brokerURL,
		WorkloadCredentialFile: credentialFile,
		PublicBaseURL:          publicBaseURL,
		snapshot:               contents,
	}, nil
}

// DecodeConnectionConfiguration reads one connector's non-secret configuration from the pinned hosted snapshot.
func (configuration Configuration) DecodeConnectionConfiguration(
	connectorID string,
	connectionName string,
	destination any,
) error {
	if configuration.Mode != ModeHosted || len(configuration.snapshot) == 0 {
		return fmt.Errorf("hosted connector configuration snapshot is required")
	}
	if destination == nil {
		return fmt.Errorf("connector configuration destination is required")
	}
	snapshot, err := decodeHostedSnapshot(configuration.snapshot)
	if err != nil {
		return err
	}
	for _, contents := range snapshot.Connections {
		var connection hostedConnection
		if err := json.Unmarshal(contents, &connection); err != nil {
			return fmt.Errorf("decode hosted connector connection: %w", err)
		}
		if connection.ConnectorID == connectorID && connection.ConnectionName == connectionName {
			return decodeStrict(connection.Configuration, destination)
		}
	}
	return fmt.Errorf("connector %q connection %q is not configured", connectorID, connectionName)
}

// DecodeTriggerConfiguration reads one Trigger binding's non-secret configuration from the pinned hosted snapshot.
func (configuration Configuration) DecodeTriggerConfiguration(
	connectorID string,
	connectionName string,
	triggerName string,
	bindingName string,
	destination any,
) error {
	if configuration.Mode != ModeHosted || len(configuration.snapshot) == 0 {
		return fmt.Errorf("hosted connector configuration snapshot is required")
	}
	if destination == nil {
		return fmt.Errorf("Trigger configuration destination is required")
	}
	snapshot, err := decodeHostedSnapshot(configuration.snapshot)
	if err != nil {
		return err
	}
	for _, contents := range snapshot.TriggerBindings {
		var binding hostedTriggerBinding
		if err := json.Unmarshal(contents, &binding); err != nil {
			return fmt.Errorf("decode hosted connector Trigger binding: %w", err)
		}
		if binding.ConnectorID == connectorID &&
			binding.ConnectionName == connectionName &&
			binding.TriggerName == triggerName &&
			binding.BindingName == bindingName {
			return decodeStrict(binding.Configuration, destination)
		}
	}
	return fmt.Errorf(
		"connector %q connection %q Trigger %q binding %q is not configured",
		connectorID, connectionName, triggerName, bindingName,
	)
}

func decodeHostedSnapshot(contents []byte) (hostedSnapshot, error) {
	var snapshot hostedSnapshot
	if err := decodeStrict(contents, &snapshot); err != nil {
		return hostedSnapshot{}, fmt.Errorf("decode hosted connector configuration snapshot: %w", err)
	}
	if snapshot.ConfigurationRevision == "" || snapshot.ConfigurationState == "" {
		return hostedSnapshot{}, fmt.Errorf("hosted connector configuration snapshot identity is incomplete")
	}
	return snapshot, nil
}

func decodeStrict(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON value has trailing content")
	}
	return nil
}

func hasHostedEnvironment() bool {
	for _, name := range []string{
		"SUPERVERSE_CONNECTOR_CONFIG_DIGEST",
		"SUPERVERSE_CONNECTOR_BROKER_URL",
		"SUPERVERSE_CONNECTOR_WORKLOAD_CREDENTIAL_FILE",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func validatedHTTPURL(name string, allowHTTP bool) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must be an absolute URL without credentials, query, or fragment", name)
	}
	if parsed.Scheme != "https" && (!allowHTTP || parsed.Scheme != "http") {
		return "", fmt.Errorf("%s must use HTTPS", name)
	}
	return strings.TrimRight(value, "/"), nil
}

func readBoundedRegularFile(path string, maximumBytes int64, requirePrivate bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumBytes {
		return nil, fmt.Errorf("file must be regular and contain between 1 and %d bytes", maximumBytes)
	}
	if requirePrivate && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("file permissions must not grant group or other access")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) != info.Size() {
		return nil, fmt.Errorf("file changed while it was being read")
	}
	return contents, nil
}
