package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	kilocodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilocode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// KilocodeExecutor is a stateless executor for Kilo Code API using OpenAI-compatible chat completions.
type KilocodeExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewKilocodeExecutor creates a new Kilo Code executor.
func NewKilocodeExecutor(cfg *config.Config) *KilocodeExecutor {
	return &KilocodeExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *KilocodeExecutor) Identifier() string { return "kilocode" }

// PrepareRequest injects Kilo Code credentials into the outgoing HTTP request.
func (e *KilocodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := kilocodeCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if orgID := kilocodeOrgID(auth); orgID != "" {
		req.Header.Set("X-Kilocode-OrganizationID", orgID)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kilo Code credentials into the request and executes it.
func (e *KilocodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kilocode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat completion request to Kilo Code.
func (e *KilocodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	from := opts.SourceFormat
	if from.String() == "claude" {
		auth.Attributes["base_url"] = kilocodeauth.ChatCompletionsURL
		return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := kilocodeCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	body, err = sjson.SetBytes(body, "model", baseModel)
	if err != nil {
		return resp, fmt.Errorf("kilocode executor: failed to set model in payload: %w", err)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "kilocode", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, kilocodeauth.ChatCompletionsURL, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyKilocodeHeaders(httpReq, token, false, auth)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       kilocodeauth.ChatCompletionsURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kilocode executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to Kilo Code.
func (e *KilocodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	if from.String() == "claude" {
		auth.Attributes["base_url"] = kilocodeauth.ChatCompletionsURL
		return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := kilocodeCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	body, err = sjson.SetBytes(body, "model", baseModel)
	if err != nil {
		return nil, fmt.Errorf("kilocode executor: failed to set model in payload: %w", err)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "kilocode", e.Identifier())
	if err != nil {
		return nil, err
	}

	body, err = sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return nil, fmt.Errorf("kilocode executor: failed to set stream_options in payload: %w", err)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, kilocodeauth.ChatCompletionsURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyKilocodeHeaders(httpReq, token, true, auth)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       kilocodeauth.ChatCompletionsURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kilocode executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kilocode executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if shouldSkipKilocodeSSEMetaLine(trimmedLine) {
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(trimmedLine), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("data: [DONE]"), &param)
			for i := range doneChunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// Refresh is a no-op for Kilo Code since it has no refresh mechanism.
func (e *KilocodeExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kilocode executor: refresh called (no-op, no refresh support)")
	return auth, nil
}

// applyKilocodeHeaders sets required headers for Kilo Code API requests.
func applyKilocodeHeaders(r *http.Request, token string, stream bool, auth *cliproxyauth.Auth) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if orgID := kilocodeOrgID(auth); orgID != "" {
		r.Header.Set("X-Kilocode-OrganizationID", orgID)
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Cache-Control", "no-cache")
		return
	}
	r.Header.Set("Accept", "application/json")
}

// kilocodeCreds extracts the access token from auth.
func kilocodeCreds(a *cliproxyauth.Auth) (token string) {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	if a.Attributes != nil {
		if v := a.Attributes["access_token"]; v != "" {
			return v
		}
		if v := a.Attributes["api_key"]; v != "" {
			return v
		}
	}
	return ""
}

// kilocodeUseOrgBilling reports whether requests should bill against the
// organization wallet. By default Kilo Code uses personal credits; sending
// X-Kilocode-OrganizationID switches billing to the org balance.
func kilocodeUseOrgBilling(a *cliproxyauth.Auth) bool {
	if a == nil {
		return false
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["use_org_billing"].(bool); ok && v {
			return true
		}
		if v, ok := a.Metadata["use-organization-billing"].(bool); ok && v {
			return true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"use_org_billing", "use-organization-billing"} {
			if v := strings.TrimSpace(a.Attributes[key]); strings.EqualFold(v, "true") || v == "1" {
				return true
			}
		}
	}
	return false
}

// kilocodeOrgID extracts the organization ID from auth metadata when org billing is enabled.
func kilocodeOrgID(a *cliproxyauth.Auth) string {
	if !kilocodeUseOrgBilling(a) {
		return ""
	}
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["org_id"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if a.Attributes != nil {
		if v := a.Attributes["org_id"]; v != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func shouldSkipKilocodeSSEMetaLine(trimmedLine []byte) bool {
	if len(trimmedLine) == 0 {
		return true
	}
	if bytes.HasPrefix(trimmedLine, []byte("data:")) {
		return false
	}
	return bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
		bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:"))
}
