package kilocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// apiBaseURL is the base URL for Kilo Code API.
	apiBaseURL = "https://api.kilo.ai"
	// initiateURL is the endpoint for requesting device codes.
	initiateURL = apiBaseURL + "/api/device-auth/codes"
	// pollURLBase is the base URL for polling device code status.
	pollURLBase = apiBaseURL + "/api/device-auth/codes"
	// profileURL is the endpoint for fetching user profile (including org ID).
	profileURL = apiBaseURL + "/api/profile"
	// ChatCompletionsURL is the upstream chat completions endpoint.
	ChatCompletionsURL = apiBaseURL + "/api/openrouter/chat/completions"
	// defaultPollInterval is the default interval for polling token endpoint.
	defaultPollInterval = 3 * time.Second
	// maxPollDuration is the maximum time to wait for user authorization.
	maxPollDuration = 5 * time.Minute
)

// KilocodeAuth handles Kilo Code authentication flow.
type KilocodeAuth struct {
	client *http.Client
	cfg    *config.Config
}

// NewKilocodeAuth creates a new KilocodeAuth service instance.
func NewKilocodeAuth(cfg *config.Config) *KilocodeAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
	}
	client = util.SetProxy(&sdkCfg, client)
	return &KilocodeAuth{
		client: client,
		cfg:    cfg,
	}
}

// StartDeviceFlow initiates the device flow by requesting a device code from Kilo Code.
func (k *KilocodeAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, initiateURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kilocode: device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kilocode device code: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to read device code response: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("kilocode: too many pending authorization requests, please try again later")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kilocode: device code request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(bodyBytes, &deviceCode); err != nil {
		return nil, fmt.Errorf("kilocode: failed to parse device code response: %w", err)
	}

	return &deviceCode, nil
}

// WaitForAuthorization polls the device code endpoint until the user authorizes or the code expires.
func (k *KilocodeAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*KilocodeTokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("kilocode: device code is nil")
	}

	interval := defaultPollInterval
	deadline := time.Now().Add(maxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("kilocode: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("kilocode: device code expired")
			}

			token, pollErr, shouldContinue := k.pollDeviceCode(ctx, deviceCode.Code)
			if token != nil {
				return token, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
		}
	}
}

// pollDeviceCode attempts to poll the device code for an access token.
// Returns (token, error, shouldContinue).
func (k *KilocodeAuth) pollDeviceCode(ctx context.Context, code string) (*KilocodeTokenData, error, bool) {
	pollURL := pollURLBase + "/" + code

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to create poll request: %w", err), false
	}
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kilocode: poll request failed: %w", err), false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kilocode poll: close body error: %v", errClose)
		}
	}()

	switch resp.StatusCode {
	case http.StatusAccepted:
		// Still pending
		return nil, nil, true
	case http.StatusForbidden:
		return nil, fmt.Errorf("kilocode: authorization denied by user"), false
	case http.StatusGone:
		return nil, fmt.Errorf("kilocode: device code expired"), false
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kilocode: poll failed with status %d: %s", resp.StatusCode, string(bodyBytes)), false
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to read poll response: %w", err), false
	}

	var pollResp struct {
		Status    string `json:"status"`
		Token     string `json:"token"`
		UserEmail string `json:"userEmail"`
	}

	if err = json.Unmarshal(bodyBytes, &pollResp); err != nil {
		return nil, fmt.Errorf("kilocode: failed to parse poll response: %w", err), false
	}

	if pollResp.Status != "approved" || pollResp.Token == "" {
		return nil, nil, true
	}

	// Fetch profile to optionally capture org ID for users who opt into org billing.
	// Personal credits are the default; org_id is only sent when use_org_billing is set.
	orgID := ""
	profileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err == nil {
		profileReq.Header.Set("Authorization", "Bearer "+pollResp.Token)
		profileReq.Header.Set("Accept", "application/json")
		profileResp, profileErr := k.client.Do(profileReq)
		if profileErr == nil {
			defer func() {
				if errClose := profileResp.Body.Close(); errClose != nil {
					log.Errorf("kilocode profile: close body error: %v", errClose)
				}
			}()
			if profileResp.StatusCode == http.StatusOK {
				profileBody, _ := io.ReadAll(profileResp.Body)
				var profile struct {
					Organizations []struct {
						ID string `json:"id"`
					} `json:"organizations"`
				}
				if json.Unmarshal(profileBody, &profile) == nil && len(profile.Organizations) > 0 {
					orgID = strings.TrimSpace(profile.Organizations[0].ID)
				}
			}
		}
	}

	return &KilocodeTokenData{
		AccessToken: pollResp.Token,
		Email:       strings.TrimSpace(pollResp.UserEmail),
		OrgID:       orgID,
	}, nil, false
}
