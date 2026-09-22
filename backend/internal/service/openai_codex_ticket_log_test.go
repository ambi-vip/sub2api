package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

func ticketLogEvents(sink *inMemoryLogSink, message string) []*logger.LogEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var events []*logger.LogEvent
	for _, event := range sink.events {
		if event.Message == message {
			events = append(events, event)
		}
	}
	return events
}

func TestCodexTicketLogMintReuseAndInvalidation(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	account := ticketTestAccount(41)
	proxy := "http://private-user:private-password@mint.example:8080"
	state := fakeCodexTicketState(332)
	calls := 0
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			require.Empty(t, req.Header.Get("Cookie"))
			require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
			h := http.Header{"Set-Cookie": {"__cflb=secret-cflb", "__oailb=secret-oailb"}}
			h.Set(openAICodexTurnStateHeader, state)
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(ticketPairTestResponse("gpt-6-astra")))}, nil
		}
		require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
		require.Equal(t, "__cflb=secret-cflb; __oailb=secret-oailb", req.Header.Get("Cookie"))
		model := "gpt-6-astra"
		if calls == 5 {
			model = "gpt-5.6-luna"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(ticketPairTestResponse(model)))}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: proxy}, upstream)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	for i := 0; i < 4; i++ {
		req, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra","input":"private-prompt"}`))
		require.NoError(t, err)
		require.NoError(t, svc.applyOpenAICodexTicketRequest(context.Background(), account, "gpt-6-astra", req))
		resp, err := svc.doOpenAIUpstream(req, "", account)
		require.NoError(t, err)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.Equal(t, 5, calls, "one mint and four generations")
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	mints := ticketLogEvents(sink, "openai_codex_ticket harvested")
	gens := ticketLogEvents(sink, "openai_codex_ticket generation")
	invalidations := ticketLogEvents(sink, "openai_codex_ticket invalidated")
	require.Len(t, mints, 1)
	require.Len(t, gens, 4, "EOF and Close must not double-log")
	require.Len(t, invalidations, 1)
	for i, event := range gens {
		require.Equal(t, mints[0].Fields["bundle_id"], event.Fields["bundle_id"])
		require.Equal(t, "http://mint.example:8080", event.Fields["proxy_endpoint"])
		if i < 3 {
			require.Equal(t, "ok", event.Fields["reason"])
		} else {
			require.Equal(t, "response_model_mismatch", event.Fields["reason"])
			require.Equal(t, "warn", event.Level)
		}
	}
	encoded, err := json.Marshal(sink.events)
	require.NoError(t, err)
	for _, secret := range []string{state, "secret-cflb", "secret-oailb", "private-user", "private-password", "private-prompt"} {
		require.NotContains(t, string(encoded), secret)
	}
}

func TestCodexTicketLogHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
		err                error
		closeEarly         bool
	}{
		{name: "http", status: 503, reason: "http_status"},
		{name: "transport", err: errors.New("http://user:secret-password@proxy.invalid"), reason: "transport_or_read_error"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "transport_or_read_error"},
		{name: "truncated", status: 200, body: `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}` + "\n\n", reason: "response_incomplete"},
		{name: "closed", status: 200, body: ticketPairTestResponse("gpt-6-astra"), closeEarly: true, reason: "response_incomplete"},
		{name: "missing model", status: 200, body: `{"status":"completed"}`, reason: "missing_response_model"},
		{name: "failed", status: 200, body: `{"type":"response.failed"}`, reason: "response_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, cleanup := captureStructuredLog(t)
			defer cleanup()
			account := ticketTestAccount(41)
			ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://proxy.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}})
			svc.storeOpenAICodexTicket(context.Background(), account, ticket)
			req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			require.NoError(t, svc.applyOpenAICodexTicketRequest(context.Background(), account, ticket.Model, req))
			resp, _ := svc.doOpenAIUpstream(req, "", account)
			if resp != nil {
				if !tc.closeEarly {
					_, _ = io.ReadAll(resp.Body)
				}
				require.NoError(t, resp.Body.Close())
			}
			events := ticketLogEvents(sink, "openai_codex_ticket generation")
			require.Len(t, events, 1)
			require.Equal(t, tc.reason, events[0].Fields["reason"])
			encoded, err := json.Marshal(events)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "secret-password")
		})
	}
}

func TestCodexTicketLogInvalidationPersistenceFailure(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	account := ticketTestAccount(41)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://proxy.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.storeOpenAICodexTicket(context.Background(), account, ticket)
	svc.accountRepo = &codexTicketLifecycleRepo{persist: func(context.Context) error { return errors.New("DB failed: secret-persisted-cookie") }}
	svc.invalidateOpenAICodexTicket(account, ticket, []byte(`{"model":"gpt-5.6-luna"}`))
	events := ticketLogEvents(sink, "openai_codex_ticket invalidate persist failed")
	require.Len(t, events, 1)
	require.Equal(t, "warn", events[0].Level)
	require.False(t, svc.lookupOpenAICodexTicket(account, ticket.Model).valid(time.Now(), 292))
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-persisted-cookie")
}

func TestCodexTicketLogWebSocketUsesWholeTurnModel(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(41)
	ticket := &openAICodexTicket{Model: "gpt-6-astra"}
	models := &upstreamResponseModelObserver{}
	created := []byte(`{"type":"response.created","response":{"model":"gpt-6-astra"}}`)
	models.ObserveOpenAI(created, "response.created")
	svc.observeOpenAICodexTicketWS(account, ticket, created, models)
	require.Empty(t, ticketLogEvents(sink, "openai_codex_ticket generation"))
	svc.observeOpenAICodexTicketWS(account, ticket, []byte(`{"type":"response.completed"}`), models)
	models.Observe("gpt-5.6-luna", true)
	models.Observe("gpt-6-astra", true)
	svc.observeOpenAICodexTicketWS(account, ticket, []byte(`{"type":"response.completed"}`), models)
	events := ticketLogEvents(sink, "openai_codex_ticket generation")
	require.Len(t, events, 2)
	require.Equal(t, "ok", events[0].Fields["reason"])
	require.Equal(t, "websocket", events[0].Fields["transport"])
	require.Equal(t, "response_model_mismatch", events[1].Fields["reason"])
	require.Equal(t, true, events[1].Fields["model_conflict"])
}

func TestCodexTicketLogProbeFailureRetainsHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
	}{
		{"http", `{"error":"secret-upstream-error"}`, "http_status", 503},
		{"truncated", `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}`, "probe_error", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, cleanup := captureStructuredLog(t)
			defer cleanup()
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://user:secret-proxy@mint.example:8080"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				response := codexTicketResponse()
				response.StatusCode = tc.status
				response.Body = io.NopCloser(strings.NewReader(tc.body))
				return response, nil
			}})
			svc.probeOnceOpenAICodexTicket(context.Background(), ticketTestAccount(41), "gpt-6-astra")
			events := ticketLogEvents(sink, "openai_codex_ticket probe miss")
			require.Len(t, events, 1)
			require.Equal(t, tc.reason, events[0].Fields["reason"])
			require.EqualValues(t, tc.status, events[0].Fields["http"])
			require.EqualValues(t, 292, events[0].Fields["length"])
			require.Equal(t, true, events[0].Fields["cookie_present"])
			encoded, err := json.Marshal(events)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "secret-proxy")
			require.NotContains(t, string(encoded), "secret-upstream-error")
		})
	}
}
