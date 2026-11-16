//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newCPABPSHandler(t *testing.T, upstream service.HTTPUpstream, loadBatch bool) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{}
	for _, id := range []int64{1, 2} {
		accounts = append(accounts, service.Account{ID: id, Name: "CPA instance", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131}, Concurrency: 2,
			Credentials: map[string]any{"base_url": "https://cpa.example.invalid/v1", "api_key": "synthetic-client-key"}, Extra: map[string]any{service.CPABPSExtraKey: true}})
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
	concurrency := service.NewConcurrencyService(&helperConcurrencyCacheStub{accountSeq: []bool{true, true, true}})
	gateway := service.NewOpenAIGatewayService(excelBPSFailoverAccountRepo{openAIImagesFailoverAccountRepo{accounts: accounts}},
		nil, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 10
	return h
}

func TestCPABPSPublicResponsesPreservesOriginalHistory(t *testing.T) {
	for _, loadBatch := range []bool{false, true} {
		for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
			for _, body := range []string{
				`{"model":"gpt-6-astra","stream":true,"input":[{"type":"compaction_trigger"}]}`,
				`{"model":"gpt-6-astra","stream":false,"reasoning":{"effort":"none"},"input":[{"type":"function_call_output","output":"orphan"}],"tools":[{"type":"custom","name":"patch","format":{"type":"text"}}]}`,
				`{"model":"gpt-6-astra","input":[{"type":"function_call_output","name":"create_thread","namespace":"mcp__codex_app","output":"<codex_delegation>handoff context</codex_delegation>"},{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`,
			} {
				up := &excelBPSFailoverUpstream{answer: func(int) *http.Response {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[]}`))}
				}}
				h := newCPABPSHandler(t, up, loadBatch)
				c, w := newExcelBPSFailoverTestContext(context.Background(), false)
				c.Request.URL.Path = path
				c.Request.Body = io.NopCloser(strings.NewReader(body))
				c.Request.ContentLength = int64(len(body))
				h.Responses(c)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Len(t, up.calls(), 1)
				require.JSONEq(t, body, string(up.bodies[0]))
				require.Equal(t, "https://cpa.example.invalid"+path, up.urls[0])
			}
		}
	}
}

func TestCPABPSPublicResponsesNeverSwitchesAccount(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502, 503} {
		up := &excelBPSFailoverUpstream{answer: func(int) *http.Response {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"cpa_failure"}}`))}
		}}
		h := newCPABPSHandler(t, up, true)
		c, w := newExcelBPSFailoverTestContext(context.Background(), false)
		h.Responses(c)
		require.Equal(t, status, w.Code, w.Body.String())
		require.Len(t, up.calls(), 1)
		require.JSONEq(t, `{"error":{"code":"cpa_failure"}}`, w.Body.String())
	}
}

// Runs the checked-in original sanitizer on the bytes actually emitted by the
// public sub2api handler. No plugin, account, external network or inference.
func TestCPABPSOriginalProxyOwnsSanitization(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 required for the original compatibility proxy")
	}
	proxy, err := filepath.Abs("../../../deploy/cpa-bps/assets/bps-sanitize-proxy.py")
	require.NoError(t, err)
	body := `{"model":"gpt-6-astra","input":[{"type":"function_call_output","namespace":"mcp__codex_app","name":"create_thread","output":"handoff context"},{"type":"custom_tool_call","call_id":"patch1","name":"patch","input":"exact patch"},{"type":"custom_tool_call_output","call_id":"patch1","output":"ok"},{"type":"function_call_output","call_id":"unknown","output":"orphan"},{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__cua_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{"code":{"type":"string"},"completion_summary":{"type":"string"}},"additionalProperties":false,"oneOf":[{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false},{"type":"object","properties":{"completion_summary":{"type":"string"}},"required":["completion_summary"],"additionalProperties":false}]}}]}]}]}`
	up := &excelBPSFailoverUpstream{answer: func(int) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[]}`))}
	}}
	h := newCPABPSHandler(t, up, true)
	c, w := newExcelBPSFailoverTestContext(context.Background(), false)
	c.Request.Body = io.NopCloser(strings.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	h.Responses(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, up.calls(), 1)
	require.JSONEq(t, body, string(up.bodies[0]))
	cmd := exec.Command(python, "-B", "-c", `import importlib.util,sys,contextlib
spec=importlib.util.spec_from_file_location('proxy',sys.argv[1])
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
with contextlib.redirect_stdout(sys.stderr):
 body=p.sanitize_body(sys.stdin.buffer.read())[0]
sys.stdout.buffer.write(body)`, proxy)
	cmd.Stdin = bytes.NewReader(up.bodies[0])
	sanitized, err := cmd.Output()
	require.NoError(t, err)
	require.True(t, gjson.ValidBytes(sanitized), string(sanitized))
	require.Len(t, gjson.GetBytes(sanitized, "input").Array(), 3)
	require.Equal(t, "patch1", gjson.GetBytes(sanitized, "input.0.call_id").String())
	require.Equal(t, "ok", gjson.GetBytes(sanitized, "input.1.output").String())
	require.NotContains(t, string(sanitized), "handoff context")
	require.NotContains(t, string(sanitized), "orphan")
	schema := gjson.GetBytes(sanitized, "input.2.tools.0.tools.0.parameters")
	require.False(t, schema.Get("properties").Exists())
	require.Equal(t, "code", schema.Get("oneOf.0.required.0").String())
	require.Equal(t, "completion_summary", schema.Get("oneOf.1.required.0").String())
}

func TestCPABPSPublicResponsesContinuation(t *testing.T) {
	for _, loadBatch := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("load_batch=%t/stream=%t", loadBatch, stream), func(t *testing.T) {
				up := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
					payload := fmt.Sprintf(`{"id":"resp_cpa_turn_%d","status":"completed","output":[]}`, call)
					contentType := "application/json"
					if stream {
						payload = "data: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
						contentType = "text/event-stream"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}
				}}
				h := newCPABPSHandler(t, up, loadBatch)
				c, w := newExcelBPSFailoverTestContext(context.Background(), stream)
				h.Responses(c)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Len(t, up.calls(), 1)
				body := fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"previous_response_id":"resp_cpa_turn_0","input":[{"type":"function_call_output","call_id":"call_original","output":"done"}]}`, stream)
				c, w = newExcelBPSFailoverTestContext(context.Background(), stream)
				c.Request.Body = io.NopCloser(strings.NewReader(body))
				c.Request.ContentLength = int64(len(body))
				h.Responses(c)
				require.Equal(t, 200, w.Code, w.Body.String())
				calls := up.calls()
				require.Len(t, calls, 2)
				require.Equal(t, calls[0], calls[1], "continuation must return to the same CPA instance")
				require.JSONEq(t, body, string(up.bodies[1]))
				// An unrelated tenant must still be rejected before reaching CPA.
				c, w = newExcelBPSFailoverTestContext(context.Background(), stream)
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 101})
				c.Request.Body = io.NopCloser(strings.NewReader(body))
				c.Request.ContentLength = int64(len(body))
				h.Responses(c)
				require.Equal(t, 400, w.Code)
				require.Contains(t, w.Body.String(), "previous_response_id is not available")
				require.Len(t, up.calls(), 2)
			})
		}
	}
}

func TestCPABPSPublicTruncatedStreamRecordedAsFailure(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	up := &excelBPSFailoverUpstream{answer: func(int) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
	}}
	h := newCPABPSHandler(t, up, true)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		fixture, _ := newExcelBPSFailoverTestContext(context.Background(), true)
		for key, value := range fixture.Keys {
			c.Set(key, value)
		}
		h.Responses(c)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, wire, w.Body.String(), "Ops failure marking must not append a synthetic SSE event")
	require.Len(t, up.calls(), 1)
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, http.StatusBadGateway, job.entry.StatusCode)
	require.NotNil(t, job.entry.UpstreamStatusCode)
	require.Equal(t, 200, *job.entry.UpstreamStatusCode)
	require.Contains(t, job.entry.ErrorBody, "cpa_bps_stream_failed")
	require.NotContains(t, job.entry.ErrorMessage, "Recovered")
}
