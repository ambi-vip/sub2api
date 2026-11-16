package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cpaBPSAccount() *Account {
	return &Account{ID: 71, Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://cpa.example.invalid/v1", "api_key": "synthetic-cpa-key", "api_protocol": APIProtocolResponses}, Extra: map[string]any{CPABPSExtraKey: true}}
}

func cpaBPSContext(body, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Set("api_key", &APIKey{ID: 42, AllowLongContext: true})
	return c, w
}

func TestCPABPSConfiguration(t *testing.T) {
	a := cpaBPSAccount()
	require.NoError(t, ValidateCPABPSAccount(a))
	require.True(t, a.IsCPABPSEnabled())
	require.False(t, a.IsExcelBPSEnabled())
	require.True(t, a.IsOpenAIWSForceHTTPEnabled())
	require.False(t, a.IsOpenAIResponsesWebSocketV2Enabled())
	require.Equal(t, OpenAIWSIngressModeOff, a.ResolveOpenAIResponsesWebSocketV2Mode(OpenAIWSIngressModePassthrough))
	require.False(t, shouldForwardOpenAIResponsesViaChatCompletions(a, []byte(`{}`)))
	require.Equal(t, APIProtocolResponses, a.GetAPIProtocol())
	for name, mutate := range map[string]func(*Account){
		"oauth":    func(a *Account) { a.Type = AccountTypeOAuth },
		"platform": func(a *Account) { a.Platform = PlatformAnthropic },
		"flag":     func(a *Account) { a.Extra[CPABPSExtraKey] = "true" },
		"url":      func(a *Account) { a.Credentials["base_url"] = "" },
		"userinfo": func(a *Account) { a.Credentials["base_url"] = "https://secret@cpa.example/v1" },
		"key":      func(a *Account) { a.Credentials["api_key"] = "" },
		"protocol": func(a *Account) { a.Credentials["api_protocol"] = APIProtocolChatCompletions },
		"copilot":  func(a *Account) { a.Extra["openai_copilot_sdk"] = true },
		"excel":    func(a *Account) { a.Extra["openai_excel_bps"] = true },
		"siwc":     func(a *Account) { a.Extra[OpenAISIWCEnabledKey] = true },
	} {
		t.Run(name, func(t *testing.T) {
			a := cpaBPSAccount()
			mutate(a)
			err := ValidateCPABPSAccount(a)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-cpa-key")
		})
	}
	merged := mergeCPABPSAccountSettings(a, nil, map[string]any{"openai_copilot_sdk": true})
	require.Error(t, ValidateCPABPSAccount(merged))
	require.NoError(t, ValidateCPABPSAccount(a))
}

func TestCPABPSPreservesProtocolAndCompact(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			body := `{"model":"test-model","reasoning":{"effort":"ultra"},"stream":false,"tools":[{"type":"namespace","name":"mcp__cua_repl","tools":[{"type":"function","name":"js","parameters":{"oneOf":[{"required":["code"]},{"required":["completion_summary"]}]}}]},{"type":"custom","name":"patch"}],"input":[{"type":"function_call_output","namespace":"codex_app","name":"create_thread","output":"<codex_delegation>task</codex_delegation>"},{"type":"custom_tool_call","call_id":"call_1","name":"patch","input":"exact bytes\n"},{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"},{"type":"additional_tools","tools":[]},{"type":"reasoning","encrypted_content":"opaque"}]}`
			response := `{"id":"resp_cpa","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}`
			if strings.HasSuffix(path, "/compact") {
				response = `{"id":"resp_compact","object":"response.compaction","output":[],"usage":{"input_tokens":8,"output_tokens":2}}`
			}
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
			svc := openAIClientToolsTestService(up)
			c, w := cpaBPSContext(body, path)
			c.Request.Header.Set("Authorization", "Bearer downstream-key")
			result, err := svc.Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(body))
			require.NoError(t, err)
			require.Len(t, up.requests, 1)
			require.JSONEq(t, body, string(up.lastBody))
			require.Equal(t, path, up.lastReq.URL.Path)
			require.Nil(t, up.lastReq.GetBody)
			require.Equal(t, "Bearer synthetic-cpa-key", up.lastReq.Header.Get("Authorization"))
			require.Equal(t, response, w.Body.String())
			require.Equal(t, 8, result.Usage.InputTokens)
			require.False(t, result.UsageUnavailable)
		})
	}
}

func TestCPABPSIsolationAndMapping(t *testing.T) {
	body := []byte(`{"model":"alias","session_id":"same","client_metadata":{"thread_id":"child"},"input":[{"type":"function_call","call_id":"original","arguments":"{\"session_id\":\"do-not-change\"}"}]}`)
	c, _ := cpaBPSContext(string(body), "/v1/responses")
	headers := http.Header{"Session_id": {"same"}, "Thread-Id": {"child"}, "X-Codex-Turn-Metadata": {`{"thread_id":"child","turn_id":"turn-1"}`}}
	first, err := isolateCPABPSRequest(c, cpaBPSAccount(), body, headers)
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(first, "session_id").String(), headers.Get("session_id"))
	require.Equal(t, gjson.GetBytes(first, "client_metadata.thread_id").String(), headers.Get("thread-id"))
	require.Equal(t, "original", gjson.GetBytes(first, "input.0.call_id").String())
	require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(first, "input").Raw)
	again, err := isolateCPABPSRequest(c, cpaBPSAccount(), body, make(http.Header))
	require.NoError(t, err)
	require.Equal(t, first, again)
	c.Set("api_key", &APIKey{ID: 43})
	other, err := isolateCPABPSRequest(c, cpaBPSAccount(), body, make(http.Header))
	require.NoError(t, err)
	require.NotEqual(t, first, other)
	a := cpaBPSAccount()
	a.ID++
	otherAccount, err := isolateCPABPSRequest(c, a, body, make(http.Header))
	require.NoError(t, err)
	require.NotEqual(t, other, otherAccount)
}

func TestCPABPSNeverRetries(t *testing.T) {
	for _, status := range []int{301, 400, 401, 403, 429, 500, 502, 503} {
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"upstream_failure"}}`))}}
		c, w := cpaBPSContext(`{"model":"test"}`, "/v1/responses")
		_, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(`{"model":"test"}`))
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.NotErrorAs(t, err, &failover)
		require.Equal(t, status, w.Code)
		require.Len(t, up.requests, 1)
	}
}

func TestCPABPSTerminalsAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		success       bool
	}{
		{"completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":2},"output":[]}}`, true},
		{"failed", `{"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":7,"output_tokens":2}}}`, false},
		{"incomplete", `{"type":"response.incomplete","response":{"status":"incomplete"}}`, false},
		{"contradictory", `{"type":"response.completed","response":{"status":"failed"}}`, false},
		{"completed_error", `{"type":"response.completed","response":{"status":"completed","error":{"code":"failed"}}}`, false},
		{"failed_then_completed", `{"type":"response.failed","response":{"status":"failed"}}` + "\r\n\r\ndata: " + `{"type":"response.completed","response":{"status":"completed"}}`, false},
		{"missing", `{"type":"response.output_text.delta","delta":"partial"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sse := ": heartbeat\r\n\r\ndata: " + tc.payload + "\r\n\r\n"
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
			c, w := cpaBPSContext(`{"model":"test","stream":true}`, "/v1/responses")
			result, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(`{"model":"test","stream":true}`))
			require.Equal(t, tc.success, err == nil)
			require.Equal(t, sse, w.Body.String())
			require.Len(t, up.requests, 1)
			if tc.name == "failed" {
				require.True(t, result.ObservedUsageOnError)
				require.Equal(t, 7, result.Usage.InputTokens)
			}
			if tc.name == "missing" {
				require.True(t, result.UsageUnavailable)
			}
		})
	}
	for _, body := range []string{`{"status":"failed","usage":{"input_tokens":7,"output_tokens":2}}`, `{"status":"incomplete"}`, `{"output":[]}`, `{"status":"completed","error":{"code":"failed"}}`, `not json`} {
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}
		c, w := cpaBPSContext(`{"model":"test"}`, "/v1/responses")
		_, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(`{"model":"test"}`))
		require.Error(t, err)
		require.Equal(t, 502, w.Code)
		require.Len(t, up.requests, 1)
	}
}

func TestCPABPSCancellationClosesUpstream(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: r}}
	c, _ := cpaBPSContext(`{"model":"test","stream":true}`, "/v1/responses")
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := openAIClientToolsTestService(up).Forward(ctx, c, cpaBPSAccount(), []byte(`{"model":"test","stream":true}`))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("CPA request continued after cancellation")
	}
}

func TestCPABPSModelsEmptyAndFailures(t *testing.T) {
	for _, raw := range []string{`{"data":[]}`, `{"data":[{"id":"actual-model","object":"model"}]}`} {
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(raw))}}
		svc := openAIClientToolsTestService(up)
		list, err := svc.FetchOpenAIModelsList(context.Background(), cpaBPSAccount())
		require.NoError(t, err)
		require.Equal(t, gjson.Get(raw, "data.#.id").Raw, gjson.GetBytes(list.Body, "data.#.id").Raw)
		require.Equal(t, "/v1/models", up.lastReq.URL.Path)
		require.Equal(t, "Bearer synthetic-cpa-key", up.lastReq.Header.Get("Authorization"))
	}
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(`{}`))}}
	_, err := openAIClientToolsTestService(up).FetchOpenAIModelsList(context.Background(), cpaBPSAccount())
	require.Error(t, err)
}

func TestCPABPSUnsupportedEndpoints(t *testing.T) {
	svc := openAIClientToolsTestService(nil)
	c, w := cpaBPSContext(`{}`, "/v1/chat/completions")
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, cpaBPSAccount(), []byte(`{}`), "", "")
	require.Error(t, err)
	require.Equal(t, 400, w.Code)
	c, w = cpaBPSContext(`{}`, "/v1/images/generations")
	_, err = svc.ForwardImages(context.Background(), c, cpaBPSAccount(), nil, nil, "")
	require.Error(t, err)
	require.Equal(t, 400, w.Code)
	require.False(t, errors.Is(err, context.Canceled))
}

func TestCPABPSAdminPersistenceAndConflictGuards(t *testing.T) {
	original := cpaBPSAccount()
	created, err := buildAccountForCreate(&CreateAccountInput{Platform: original.Platform, Type: original.Type, Credentials: original.Credentials}, original.Extra)
	require.NoError(t, err)
	require.True(t, created.IsCPABPSEnabled())
	repo := &upstreamBillingProbeAdminRepo{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{original.ID: original}}}
	svc := &adminServiceImpl{accountRepo: repo}
	// Public account DTOs omit api_key; a URL-only edit must retain the secret.
	updated, err := svc.UpdateAccount(context.Background(), original.ID, &UpdateAccountInput{Credentials: map[string]any{"base_url": "https://new-cpa.example/v1"}})
	require.NoError(t, err)
	require.True(t, updated.IsCPABPSEnabled())
	require.Equal(t, "synthetic-cpa-key", updated.GetCredential("api_key"))
	require.Error(t, svc.UpdateAccountExtra(context.Background(), original.ID, map[string]any{"openai_copilot_sdk": true}))
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{original.ID}, Extra: map[string]any{"openai_excel_bps": true}})
	require.Error(t, err)
	require.Empty(t, repo.bulkUpdates)
	_, err = svc.UpdateAccount(context.Background(), original.ID, &UpdateAccountInput{Type: AccountTypeOAuth})
	require.Error(t, err)
	updated, err = svc.UpdateAccount(context.Background(), original.ID, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.False(t, updated.IsCPABPSEnabled())
}

func TestCPABPSHeadersMappingAndFailureBodies(t *testing.T) {
	for _, stream := range []bool{false, true} {
		response := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"cpa_failure\"}}}\n\n"
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response))}}
		body := `{"model":"alias","stream":false}`
		if stream {
			body = `{"model":"alias","stream":true}`
		}
		c, w := cpaBPSContext(body, "/v1/responses")
		c.Request.Header.Set("Thread-Id", "same-thread")
		c.Request.Header.Set("Session-Id", "same-thread")
		a := cpaBPSAccount()
		a.Credentials["model_mapping"] = map[string]any{"alias": "actual-model"}
		_, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, a, []byte(body))
		require.Error(t, err)
		require.Equal(t, "actual-model", gjson.GetBytes(up.lastBody, "model").String())
		require.NotEmpty(t, up.lastReq.Header.Get("Thread-Id"))
		require.NotEqual(t, "same-thread", up.lastReq.Header.Get("Thread-Id"))
		require.Equal(t, up.lastReq.Header.Get("Thread-Id"), up.lastReq.Header.Get("Session-Id"))
		require.Contains(t, w.Body.String(), "cpa_failure")
		if !stream {
			require.Equal(t, 502, w.Code)
		}
	}
}

func TestCPABPSRejectsFailedCompact(t *testing.T) {
	raw := `{"object":"response.compaction","output":[],"status":"incomplete"}`
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(raw))}}
	c, w := cpaBPSContext(`{"model":"test"}`, "/v1/responses/compact")
	_, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(`{"model":"test"}`))
	require.Error(t, err)
	require.Equal(t, 502, w.Code)
}

func TestCPABPSSchedulingUsesActualEndpoint(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/v1/responses", true}, {"/responses", true}, {"/v1/responses/compact", true},
		{"/v1/chat/completions", false}, {"/v1/embeddings", false}, {"/v1/images/generations", false},
	} {
		c, _ := cpaBPSContext(`{}`, tc.path)
		ctx := WithOpenAIConnectionCapabilityForRequest(context.Background(), c)
		require.Equal(t, tc.want, cpaBPSAccount().supportsOpenAIEndpointCapabilityForRequest(ctx, OpenAIEndpointCapabilityChatCompletions), tc.path)
	}
}

func TestCPABPSProgressTimeout(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: r}}
	svc := openAIClientToolsTestService(up)
	svc.cfg.Gateway.StreamDataIntervalTimeout = 1
	c, rec := cpaBPSContext(`{"model":"test","stream":true}`, "/v1/responses")
	started := time.Now()
	_, err := svc.Forward(context.Background(), c, cpaBPSAccount(), []byte(`{"model":"test","stream":true}`))
	require.Error(t, err)
	require.Less(t, time.Since(started), 3*time.Second)
	require.Equal(t, 502, rec.Code)
	require.Len(t, up.requests, 1)
}

func TestCPABPSDedicatedGroupCatalogNeverInventsModels(t *testing.T) {
	for _, codex := range []bool{false, true} {
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}}
		svc := openAIClientToolsTestService(up)
		account := cpaBPSAccount()
		account.Credentials["model_mapping"] = map[string]any{"invented-model": "no-such-upstream-model"}
		svc.accountRepo = codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{17: {*account}}}
		group := &Group{ID: 17, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"invented-model"}}}
		var result *OpenAIModelsResponse
		var handled bool
		var err error
		if codex {
			result, handled, err = svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, "")
		} else {
			result, _, handled, err = svc.FetchCPABPSGroupModels(context.Background(), group, "")
		}
		require.NoError(t, err)
		require.True(t, handled)
		field := "data"
		if codex {
			field = "models"
		}
		require.Empty(t, gjson.GetBytes(result.Body, field).Array())
		require.Len(t, up.requests, 1)
		require.NotContains(t, string(result.Body), "invented-model")
	}
}
