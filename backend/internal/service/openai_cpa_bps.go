package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const CPABPSExtraKey = "openai_cpa_bps"

// CPABPSForwardError records delivery without making an accepted CPA request
// eligible for failover or appending a synthetic SSE error to its original body.
type CPABPSForwardError struct {
	Cause           error
	ResponseWritten bool
}

func (e *CPABPSForwardError) Error() string { return e.Cause.Error() }
func (e *CPABPSForwardError) Unwrap() error { return e.Cause }

func cpaBPSSettingsTouched(extra map[string]any) bool {
	for _, key := range []string{CPABPSExtraKey, "openai_copilot_sdk", "openai_excel_bps", OpenAISIWCEnabledKey, "openai_prism_browser"} {
		if _, ok := extra[key]; ok {
			return true
		}
	}
	return false
}

func mergeCPABPSAccountSettings(account *Account, credentials, extra map[string]any) *Account {
	if account == nil {
		return nil
	}
	merged := *account
	merged.Credentials = make(map[string]any)
	merged.Extra = make(map[string]any)
	maps.Copy(merged.Credentials, account.Credentials)
	maps.Copy(merged.Credentials, credentials)
	maps.Copy(merged.Extra, account.Extra)
	maps.Copy(merged.Extra, extra)
	return &merged
}

func (a *Account) IsCPABPSEnabled() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeAPIKey && a.Extra[CPABPSExtraKey] == true
}

// ValidateCPABPSAccount also runs on merged updates so callers cannot enable a
// conflicting mode by changing only credentials or another extra field.
func ValidateCPABPSAccount(a *Account) error {
	if a == nil {
		return nil
	}
	flag, present := a.Extra[CPABPSExtraKey]
	if !present {
		return nil
	}
	enabled, ok := flag.(bool)
	if !ok {
		return errors.New("openai_cpa_bps must be a boolean")
	}
	if !enabled {
		return nil
	}
	if !a.IsCPABPSEnabled() || a.IsShadow() {
		return errors.New("CPA-BPS requires an OpenAI API Key account")
	}
	for _, key := range []string{"openai_copilot_sdk", "openai_excel_bps", OpenAISIWCEnabledKey, "openai_prism_browser"} {
		if a.Extra[key] == true {
			return fmt.Errorf("CPA-BPS conflicts with %s", key)
		}
	}
	u, err := url.Parse(strings.TrimSpace(a.GetCredential("base_url")))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("CPA-BPS requires an HTTP(S) gateway base URL without credentials, query or fragment")
	}
	if strings.TrimSpace(a.GetCredential("api_key")) == "" {
		return errors.New("CPA-BPS requires the gateway client_api key")
	}
	if p := a.GetCredential("api_protocol"); p != "" && p != APIProtocolResponses {
		return errors.New("CPA-BPS only supports the Responses protocol")
	}
	return nil
}

func cpaBPSUnsupported(c *gin.Context) error {
	err := errors.New("CPA-BPS supports HTTP Responses and compact only; this endpoint is unavailable")
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "cpa_bps_endpoint_unsupported", "message": err.Error()}})
	return &CPABPSForwardError{Cause: err, ResponseWritten: true}
}

func cpaBPSURL(account *Account, endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + endpoint
}

// Only opaque conversation identifiers are namespaced. Tool IDs and all tool
// payloads belong to CPA and must survive unchanged, including orphan outputs.
func isolateCPABPSRequest(c *gin.Context, account *Account, body []byte, headers http.Header) ([]byte, error) {
	identity := fmt.Sprintf("cpa-bps:%d", account.ID)
	isolate := func(value string) string {
		return isolateOpenAIUpstreamSessionID(getAPIKeyIDFromContext(c), account, identity+":"+value)
	}
	for _, name := range []string{"session_id", "session-id", "conversation_id", "thread-id", "x-codex-thread-id"} {
		if value := headers.Get(name); value != "" {
			headers.Set(name, isolate(value))
		}
	}
	for _, path := range []string{"session_id", "conversation_id", "thread_id", "prompt_cache_key", "client_metadata.session_id", "client_metadata.conversation_id", "client_metadata.thread_id"} {
		if value := gjson.GetBytes(body, path); value.Type == gjson.String && value.String() != "" {
			var err error
			body, err = sjson.SetBytes(body, path, isolate(value.String()))
			if err != nil {
				return nil, err
			}
		}
	}
	if raw := headers.Get("x-codex-turn-metadata"); raw != "" {
		if !gjson.Valid(raw) {
			headers.Del("x-codex-turn-metadata")
		} else {
			for _, path := range []string{"thread_id", "session_id", "conversation_id"} {
				if value := gjson.Get(raw, path); value.Type == gjson.String && value.String() != "" {
					var err error
					raw, err = sjson.Set(raw, path, isolate(value.String()))
					if err != nil {
						return nil, err
					}
				}
			}
			headers.Set("x-codex-turn-metadata", raw)
		}
	}
	return body, nil
}

func (s *OpenAIGatewayService) forwardCPABPS(ctx context.Context, c *gin.Context, account *Account, body []byte, started time.Time) (result *OpenAIForwardResult, resultErr error) {
	initialSize := c.Writer.Size()
	defer func() {
		if resultErr != nil {
			resultErr = &CPABPSForwardError{Cause: resultErr, ResponseWritten: c.Writer.Written() && c.Writer.Size() != initialSize}
		}
	}()
	if err := ValidateCPABPSAccount(account); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "cpa_bps_configuration_invalid", "message": err.Error()}})
		return nil, err
	}
	if _, err := s.validateUpstreamBaseURL(account.GetCredential("base_url")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "cpa_bps_url_disallowed", "message": "CPA-BPS URL is not allowed by the upstream URL policy"}})
		return nil, errors.New("CPA-BPS URL is not allowed")
	}
	model := gjson.GetBytes(body, "model").String()
	mapped := account.GetMappedModel(model)
	if mapped != model {
		var err error
		body, err = sjson.SetBytes(body, "model", mapped)
		if err != nil {
			return nil, err
		}
	}
	endpoint := "/responses"
	compact := isOpenAIResponsesCompactPath(c)
	if compact {
		endpoint += "/compact"
	}
	SetActualOpenAIUpstreamEndpoint(c, "/v1"+endpoint)
	ctx = withOpenAITransportMode(ctx, "cpa_bps")
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	headers := make(http.Header)
	for key, values := range c.Request.Header {
		lower := strings.ToLower(key)
		if isOpenAIPassthroughAllowedRequestHeader(lower, false) || lower == "session-id" || lower == "thread-id" || lower == "x-codex-thread-id" {
			headers[key] = append([]string(nil), values...)
		}
	}
	var err error
	body, err = isolateCPABPSRequest(c, account, body, headers)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cpaBPSURL(account, endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers
	req.Header.Del("X-API-Key")
	req.Header.Del("X-Goog-API-Key")
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	req.Header.Set("Content-Type", "application/json")
	stream := gjson.GetBytes(body, "stream").Bool() && !compact
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// No transport replay, including repository and net/http retry paths.
	req.GetBody = nil
	proxy := ""
	if account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		setOpsUpstreamError(c, http.StatusBadGateway, "CPA-BPS transport failed; request was not replayed", "")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "cpa_bps_transport_error", "message": "CPA-BPS transport failed; request was not replayed"}})
		return nil, errors.New("CPA-BPS transport failed")
	}
	if s.cfg != nil {
		resp.Body = basispoints.WithProgressTimeout(ctx, resp.Body, time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout)*time.Second)
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stop()
	result = &OpenAIForwardResult{Model: model, BillingModel: mapped, UpstreamModel: mapped, Stream: stream, UsageUnavailable: true,
		UpstreamEndpoint: "/v1" + endpoint, UpstreamProtocol: "http", ProtocolDecisionReason: "cpa_bps", UpstreamHeaders: resp.Header.Clone(),
		RequestID: resp.Header.Get("X-Request-Id"), ReasoningEffort: extractOpenAIReasoningEffortFromBody(body, model)}
	defer func() {
		result.Duration = time.Since(started)
		result.ClientDisconnect = ctx.Err() != nil
		result.ObservedUsageOnError = resultErr != nil && !result.UsageUnavailable
		if resultErr != nil {
			if result.ClientDisconnect {
				MarkOpsClientCancellation(c, stream)
				return
			}
			setOpsUpstreamError(c, resp.StatusCode, "CPA-BPS response did not complete successfully", "")
			if stream && c.Writer.Written() && c.Writer.Status() < http.StatusBadRequest {
				// The wire may already be HTTP 200 with no terminal error frame.
				// Record the failed request without changing CPA's original SSE.
				MarkOpsStreamFailure(c, "upstream_error", "cpa_bps_stream_failed", "CPA-BPS stream did not complete successfully", http.StatusBadGateway)
			}
			return
		}
		if !compact {
			// Publish only successful Responses IDs, never partial responses or
			// compaction objects. The shared binder survives downstream cancellation.
			s.bindHTTPResponseAccount(ctx, c, account, result.ResponseID)
		}
	}()
	for _, key := range []string{"X-Request-Id", "Retry-After"} {
		if value := resp.Header.Get(key); value != "" {
			c.Header(key, value)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return result, errors.New("CPA-BPS error response interrupted")
		}
		if gjson.ValidBytes(raw) {
			observeCPABPSResponse(raw, result)
		}
		c.Data(resp.StatusCode, "application/json", raw)
		return result, fmt.Errorf("CPA-BPS rejected request: HTTP %d", resp.StatusCode)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return result, s.relayCPABPSSSE(ctx, c, resp, result, started)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "cpa_bps_response_invalid", "message": "CPA-BPS response interrupted or too large"}})
		return result, errors.New("CPA-BPS response interrupted or too large")
	}
	valid := gjson.ValidBytes(raw)
	if valid {
		observeCPABPSResponse(raw, result)
	}
	completed := valid && cpaBPSResponseCompleted(gjson.ParseBytes(raw))
	if compact && valid && gjson.GetBytes(raw, "object").String() == "response.compaction" && gjson.GetBytes(raw, "output").IsArray() && !cpaBPSResponseFailed(gjson.ParseBytes(raw)) && (gjson.GetBytes(raw, "status").String() == "" || gjson.GetBytes(raw, "status").String() == "completed") {
		completed = true
	}
	if !completed {
		if valid {
			c.Data(http.StatusBadGateway, "application/json", raw)
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "cpa_bps_response_invalid", "message": "CPA-BPS returned invalid JSON"}})
		}
		return result, errors.New("CPA-BPS response did not complete")
	}
	c.Data(resp.StatusCode, "application/json", raw)
	return result, nil
}

func cpaBPSResponseFailed(response gjson.Result) bool {
	status := response.Get("status").String()
	failure := response.Get("error")
	return status == "failed" || status == "incomplete" || status == "cancelled" || (failure.Exists() && failure.Type != gjson.Null)
}

func cpaBPSResponseCompleted(response gjson.Result) bool {
	return response.IsObject() && response.Get("status").String() == "completed" && !cpaBPSResponseFailed(response)
}

func observeCPABPSResponse(raw []byte, result *OpenAIForwardResult) {
	if usage, ok := extractOpenAIUsageFromJSONBytes(raw); ok {
		result.Usage, result.UsageUnavailable = usage, false
	}
	response := gjson.ParseBytes(raw)
	if nested := response.Get("response"); nested.IsObject() {
		response = nested
	}
	if id := response.Get("id").String(); id != "" {
		result.ResponseID = id
	}
	if model := response.Get("model").String(); model != "" {
		result.UpstreamResponseModel = model
	}
	if tier := response.Get("service_tier").String(); tier != "" {
		result.UpstreamResponseServiceTier = tier
	}
}

// Relay original SSE lines; inspecting complete frames never dispatches tools
// or converts upstream errors into retryable account failures.
func (s *OpenAIGatewayService) relayCPABPSSSE(ctx context.Context, c *gin.Context, resp *http.Response, result *OpenAIForwardResult, started time.Time) (relayErr error) {
	var finalJSON []byte
	defer func() {
		if relayErr != nil && !c.Writer.Written() && ctx.Err() == nil {
			if len(finalJSON) > 0 {
				c.Data(http.StatusBadGateway, "application/json", finalJSON)
			} else {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "cpa_bps_stream_failed", "message": "CPA-BPS stream did not complete"}})
			}
		}
	}()
	scanner := s.newUpstreamSSEScanner(resp.Body)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i+1], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var data []byte
	event, terminal := "", ""
	failed := false
	writeHeaders := s.newStreamHeaderWriter(c, resp.Header)
	consume := func() error {
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			data, event = nil, ""
			return nil
		}
		if !gjson.ValidBytes(data) {
			return errors.New("CPA-BPS returned invalid SSE JSON")
		}
		kind := gjson.GetBytes(data, "type").String()
		if kind == "" {
			kind = event
		}
		observeCPABPSResponse(data, result)
		if kind == "response.completed" || kind == "response.failed" || kind == "response.incomplete" || kind == "error" {
			wasFailed := failed
			if !failed {
				terminal = kind
				result.UpstreamTerminalEvent = kind
			}
			if kind != "response.completed" || !cpaBPSResponseCompleted(gjson.GetBytes(data, "response")) {
				failed = true
			}
			if response := gjson.GetBytes(data, "response"); response.IsObject() && !wasFailed {
				finalJSON = []byte(response.Raw)
			}
		}
		data, event = nil, ""
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			if err := consume(); err != nil {
				return err
			}
		} else if bytes.HasPrefix(trimmed, []byte("data:")) {
			part := bytes.TrimPrefix(trimmed[5:], []byte(" "))
			if len(data)+len(part) > 16<<20 {
				return errors.New("CPA-BPS SSE event exceeds limit")
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, part...)
		} else if bytes.HasPrefix(trimmed, []byte("event:")) {
			event = strings.TrimSpace(string(trimmed[6:]))
		}
		if result.Stream {
			writeHeaders()
			if _, err := c.Writer.Write(line); err != nil {
				return err
			}
			c.Writer.Flush()
		}
		if result.FirstTokenMs == nil && bytes.Contains(trimmed, []byte("response.output_text.delta")) {
			elapsed := int(time.Since(started).Milliseconds())
			result.FirstTokenMs = &elapsed
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("CPA-BPS stream interrupted")
	}
	if len(data) != 0 {
		return errors.New("CPA-BPS stream ended inside an event")
	}
	if failed || terminal != "response.completed" {
		return errors.New("CPA-BPS stream did not complete")
	}
	if !result.Stream {
		if len(finalJSON) == 0 {
			return errors.New("CPA-BPS stream has no completed response")
		}
		c.Data(http.StatusOK, "application/json", finalJSON)
	}
	return nil
}

// Empty model lists are authoritative. Avoid stale fallback and manufactured
// model IDs when the independent CPA account pool has no authorized models.
func (s *OpenAIGatewayService) fetchCPABPSModels(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
	if err := ValidateCPABPSAccount(account); err != nil {
		return nil, err
	}
	if _, err := s.validateUpstreamBaseURL(account.GetCredential("base_url")); err != nil {
		return nil, errors.New("CPA-BPS URL is not allowed")
	}
	ctx, cancel := context.WithTimeout(WithHTTPUpstreamRedirectsDisabled(ctx), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cpaBPSURL(account, "/models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpUpstream.Do(req, upstreamModelsProxyURL(account), account.ID, account.Concurrency)
	if err != nil {
		return nil, errors.New("CPA-BPS model discovery failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CPA-BPS model discovery returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, errors.New("CPA-BPS model list interrupted or too large")
	}
	body, err := standardOpenAIModelsBody(raw, false)
	if err != nil {
		return nil, errors.New("CPA-BPS returned an invalid model list")
	}
	return &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body), authoritativeCatalog: true}, nil
}

// FetchCPABPSGroupModels bypasses locally synthesized /models catalogs for a
// dedicated CPA group. Mixed groups retain their existing catalog semantics.
func (s *OpenAIGatewayService) FetchCPABPSGroupModels(ctx context.Context, group *Group, ifNoneMatch string) (*OpenAIModelsResponse, *Account, bool, error) {
	if s == nil || s.accountRepo == nil || group == nil || group.Platform != PlatformOpenAI {
		return nil, nil, false, nil
	}
	visible, catalog, err := loadCodexGroupCatalogAccounts(ctx, s.accountRepo, group.ID)
	if err != nil {
		return nil, nil, false, err
	}
	return s.fetchCPABPSCatalogForGroup(ctx, group, visible, catalog, false, ifNoneMatch)
}

func (s *OpenAIGatewayService) fetchCPABPSCatalogForGroup(ctx context.Context, group *Group, visible, catalog []Account, codex bool, ifNoneMatch string) (*OpenAIModelsResponse, *Account, bool, error) {
	if len(catalog) == 0 {
		return nil, nil, false, nil
	}
	for i := range catalog {
		if !catalog[i].IsCPABPSEnabled() {
			return nil, nil, false, nil
		}
	}
	if len(visible) == 0 {
		return nil, nil, true, errors.New("CPA-BPS group has no available instance")
	}
	account := &visible[0]
	var response *OpenAIModelsResponse
	var err error
	if codex {
		response, err = s.FetchCodexModelsManifest(ctx, account, "", "")
	} else {
		response, err = s.fetchCPABPSModels(ctx, account)
	}
	if err != nil {
		return nil, account, true, err
	}
	body, err := projectAccountModelsBody(response.Body, account, group, codex)
	if err != nil {
		return nil, account, true, err
	}
	if group.ModelAllowlistEnabled() {
		field, idField := "data", "id"
		if codex {
			field, idField = "models", "slug"
		}
		envelope, entries, parseErr := modelCatalogEntries(body, field)
		if parseErr != nil {
			return nil, account, true, parseErr
		}
		kept := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			if group.ModelAllowlist.Allows(gjson.GetBytes(entry, idField).String()) {
				kept = append(kept, entry)
			}
		}
		envelope[field], err = json.Marshal(kept)
		if err != nil {
			return nil, account, true, err
		}
		body, err = json.Marshal(envelope)
		if err != nil {
			return nil, account, true, err
		}
	}
	response.Body = body
	response.ETag = codexModelsManifestBodyETag(body)
	return openAIModelsResponseForClient(response, ifNoneMatch), account, true, nil
}
