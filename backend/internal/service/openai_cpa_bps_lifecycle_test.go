package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCPABPSContinuationBinding(t *testing.T) {
	for _, tc := range []struct {
		name, response, contentType, path string
		stream, success, bound            bool
	}{
		{"JSON", `{"id":"resp_cpa_owner","status":"completed","output":[]}`, "application/json", "/v1/responses", false, true, true},
		{"SSE", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cpa_owner\",\"status\":\"completed\",\"output\":[]}}\n\n", "text/event-stream", "/v1/responses", true, true, true},
		{"unary SSE", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cpa_owner\",\"status\":\"completed\",\"output\":[]}}\n\n", "text/event-stream", "/v1/responses", false, true, true},
		{"failed", `{"id":"resp_cpa_owner","status":"failed"}`, "application/json", "/v1/responses", false, false, false},
		{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_cpa_owner\",\"status\":\"incomplete\"}}\n\n", "text/event-stream", "/v1/responses", true, false, false},
		{"truncated", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cpa_owner\",\"status\":\"in_progress\"}}\n\n", "text/event-stream", "/v1/responses", true, false, false},
		{"compact", `{"id":"resp_cpa_owner","object":"response.compaction","output":[]}`, "application/json", "/v1/responses/compact", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"test","stream":false}`
			if tc.stream {
				body = `{"model":"test","stream":true}`
			}
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.response))}}
			svc := openAIClientToolsTestService(up)
			c, _ := cpaBPSContext(body, tc.path)
			groupID := int64(17)
			c.Set("api_key", &APIKey{ID: 42, GroupID: &groupID, AllowLongContext: true})
			SetOpenAIHTTPResponseOwner(c, 99, 42)
			account := cpaBPSAccount()
			account.GroupIDs = []int64{groupID}
			result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotNil(t, result)
			require.Equal(t, "resp_cpa_owner", result.ResponseID)
			owned, err := svc.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, result.ResponseID, 99, 42)
			require.NoError(t, err)
			require.Equal(t, tc.bound, owned)
			accountID, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), groupID, result.ResponseID)
			require.NoError(t, err)
			if tc.bound {
				require.Equal(t, cpaBPSAccount().ID, accountID)
			} else {
				require.Zero(t, accountID)
			}
			owned, err = svc.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, result.ResponseID, 100, 43)
			require.NoError(t, err)
			require.False(t, owned, "another user must not inherit the response")
			owned, err = svc.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID+1, result.ResponseID, 99, 42)
			require.NoError(t, err)
			require.False(t, owned, "another group must not inherit the response")
		})
	}
}

func TestCPABPSStreamFailureMarkedWithoutChangingWire(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		readErr    error
	}{
		{"missing terminal", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", nil},
		{"interrupted", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", io.ErrUnexpectedEOF},
		{"partial event", "data: {\"type\":\"response.output_text.delta\"", nil},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n", nil},
		{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := io.Reader(strings.NewReader(tc.wire))
			if tc.readErr != nil {
				reader = io.MultiReader(reader, cpaBPSReadError{tc.readErr})
			}
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader)}}
			body := `{"model":"test","stream":true}`
			c, w := cpaBPSContext(body, "/v1/responses")
			result, err := openAIClientToolsTestService(up).Forward(c.Request.Context(), c, cpaBPSAccount(), []byte(body))
			require.Error(t, err)
			require.Equal(t, 200, w.Code)
			require.Equal(t, tc.wire, w.Body.String())
			marked, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.True(t, marked.CountTowardsSLA)
			require.Equal(t, http.StatusBadGateway, marked.IntendedStatus)
			require.Equal(t, "cpa_bps_stream_failed", marked.Code)
			require.Equal(t, 200, marked.UpstreamStatus)
			require.Len(t, up.requests, 1)
			var forwarded *CPABPSForwardError
			require.ErrorAs(t, err, &forwarded)
			require.True(t, forwarded.ResponseWritten)
			if tc.name == "failed" {
				require.True(t, result.ObservedUsageOnError)
				require.Equal(t, 7, result.Usage.InputTokens)
			}
		})
	}
}

type cpaBPSReadError struct{ err error }

func (r cpaBPSReadError) Read([]byte) (int, error) { return 0, r.err }

func TestCPABPSClientCancellationIsNotProviderFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	reader := io.MultiReader(strings.NewReader(wire), &bpsCancelBody{cancel: cancel})
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader)}}
	body := `{"model":"test","stream":true}`
	c, w := cpaBPSContext(body, "/v1/responses")
	c.Request = c.Request.WithContext(ctx)
	result, err := openAIClientToolsTestService(up).Forward(ctx, c, cpaBPSAccount(), []byte(body))
	require.Error(t, err)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, wire, w.Body.String())
	marked, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, OpsClientCanceledCode, marked.Code)
	require.Equal(t, 499, marked.IntendedStatus)
	require.True(t, marked.RequestScoped)
	require.False(t, marked.CountTowardsSLA)
	_, providerFailure := c.Get(OpsUpstreamErrorMessageKey)
	require.False(t, providerFailure)
}

func TestCPABPSContinuationDispatchPreservesNativeGuards(t *testing.T) {
	svc := openAIClientToolsTestService(nil)
	account := cpaBPSAccount()
	body := []byte(`{"model":"test","previous_response_id":"resp_cpa_guard"}`)
	c, _ := cpaBPSContext(string(body), "/v1/responses")
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), 0, "resp_cpa_guard", account.ID, time.Minute))
	for _, tc := range []struct {
		name   string
		mutate func(*openAIAgentRecoveryRequest, *Account)
		allow  bool
	}{
		{"CPA pointer", func(*openAIAgentRecoveryRequest, *Account) {}, true},
		{"other CPA instance", func(_ *openAIAgentRecoveryRequest, a *Account) { a.ID++ }, false},
		{"ordinary passthrough", func(_ *openAIAgentRecoveryRequest, a *Account) { a.Extra = map[string]any{"openai_passthrough": true} }, false},
		{"native owner", func(r *openAIAgentRecoveryRequest, a *Account) {
			r.owner = &openAIAgentRecoveryBinding{accountID: a.ID, config: openAIAgentRecoveryConfig(a, "test")}
		}, false},
		{"rebuilt native history", func(r *openAIAgentRecoveryRequest, _ *Account) { r.omittedHistory = true }, false},
		{"preserved native history", func(r *openAIAgentRecoveryRequest, _ *Account) {
			r.disableOwnerBinding = true
			r.items = [][32]byte{{1}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := *account
			request := &openAIAgentRecoveryRequest{model: "test", previousResponseID: "resp_cpa_guard", httpResponsePointer: true}
			tc.mutate(request, &a)
			ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, request)
			err := svc.checkOpenAIAgentRecoveryDispatch(ctx, c, &a, body)
			if tc.allow {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
