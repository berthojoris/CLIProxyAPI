package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilocode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KilocodeAuthenticator implements the custom device flow login for Kilo Code.
type KilocodeAuthenticator struct{}

// NewKilocodeAuthenticator constructs a new Kilo Code authenticator.
func NewKilocodeAuthenticator() Authenticator {
	return &KilocodeAuthenticator{}
}

// Provider returns the provider key for kilocode.
func (KilocodeAuthenticator) Provider() string {
	return "kilocode"
}

// RefreshLead returns nil since Kilo Code tokens do not expire and have no refresh mechanism.
func (KilocodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login initiates the Kilo Code device flow authentication.
func (a KilocodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := kilocode.NewKilocodeAuth(cfg)

	// Start the device flow
	fmt.Println("Starting Kilo Code authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to start device flow: %w", err)
	}

	// Display the verification URL
	verificationURL := strings.TrimSpace(deviceCode.VerificationURL)
	if verificationURL == "" {
		return nil, fmt.Errorf("kilocode: no verification URL received")
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.Code != "" {
		fmt.Printf("Code: %s\n\n", deviceCode.Code)
	}

	// Try to open the browser automatically
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	// Wait for user authorization
	tokenData, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("kilocode: %w", err)
	}

	// Create the token storage
	tokenStorage := &kilocode.KilocodeTokenStorage{
		AccessToken: tokenData.AccessToken,
		Email:       tokenData.Email,
		OrgID:       tokenData.OrgID,
		Type:        "kilocode",
	}

	// Build metadata with token information
	metadata := map[string]any{
		"type":         "kilocode",
		"access_token": tokenData.AccessToken,
		"timestamp":    time.Now().UnixMilli(),
	}
	if strings.TrimSpace(tokenData.Email) != "" {
		metadata["email"] = strings.TrimSpace(tokenData.Email)
	}
	if strings.TrimSpace(tokenData.OrgID) != "" {
		metadata["org_id"] = strings.TrimSpace(tokenData.OrgID)
	}

	// Generate a unique filename
	label := "Kilo Code User"
	if tokenData.Email != "" {
		label = tokenData.Email
	}
	fileName := fmt.Sprintf("kilocode-%d.json", time.Now().UnixMilli())

	fmt.Println("\nKilo Code authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
