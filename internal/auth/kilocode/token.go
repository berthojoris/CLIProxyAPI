// Package kilocode provides authentication and token management for Kilo Code API.
// It handles the custom device authorization flow for secure authentication.
package kilocode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// KilocodeTokenStorage stores token information for Kilo Code API authentication.
type KilocodeTokenStorage struct {
	// AccessToken is the bearer token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// Email is the user email returned during device auth.
	Email string `json:"email,omitempty"`
	// OrgID is the Kilo Code organization ID.
	OrgID string `json:"org_id,omitempty"`
	// Type indicates the authentication provider type, always "kilocode".
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *KilocodeTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// KilocodeTokenData holds the raw token response from Kilo Code device auth.
type KilocodeTokenData struct {
	// AccessToken is the bearer token.
	AccessToken string `json:"access_token"`
	// Email is the user email from the device auth response.
	Email string `json:"email"`
	// OrgID is the Kilo Code organization ID.
	OrgID string `json:"org_id"`
}

// DeviceCodeResponse represents Kilo Code's device code initiation response.
type DeviceCodeResponse struct {
	// Code is the device verification code used for polling.
	Code string `json:"code"`
	// VerificationURL is the URL where the user should authorize the code.
	VerificationURL string `json:"verificationUrl"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expiresIn"`
}

// SaveTokenToFile serializes the Kilocode token storage to a JSON file.
func (ts *KilocodeTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kilocode"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired checks if the token has expired.
// Kilo Code tokens have no expiry, so this always returns false.
func (ts *KilocodeTokenStorage) IsExpired() bool {
	return false
}

// NeedsRefresh checks if the token should be refreshed.
// Kilo Code does not support refresh tokens.
func (ts *KilocodeTokenStorage) NeedsRefresh() bool {
	return false
}

// refreshTokenExpiry is a sentinel for "no expiry".
// It is exported so callers can reference it if needed.
const noExpiry = ""
