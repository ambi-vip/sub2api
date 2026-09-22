package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// fenjue consistency: reuse stops whenever the generation does not declare the
// ticket's model — a different model, or a cleanly terminated stream with no
// model at all. The pair is tombstoned (invalidated) and re-minted later; the
// generation log keeps its existing reason taxonomy.
func TestCodexTicketGenerationEndInvalidatesPair(t *testing.T) {
	for _, tc := range []struct {
		name          string
		body          string
		status        int
		err           error
		drop          bool
		reason        string
		invalidations int
	}{
		{name: "completed without model", body: `{"status":"completed"}`, status: 200, drop: true, reason: "missing_response_model", invalidations: 1},
		{name: "failed without model", body: `{"type":"response.failed"}`, status: 200, drop: true, reason: "response_failed", invalidations: 1},
		{name: "silence then clean EOF", body: `data: {"type":"response.created"}` + "\n\n", status: 200, drop: true, reason: "response_incomplete", invalidations: 1},
		{name: "model mismatch", body: ticketPairTestResponse("gpt-5.6-luna"), status: 200, drop: true, reason: "response_model_mismatch", invalidations: 1},
		{name: "ok", body: ticketPairTestResponse("gpt-6-astra"), status: 200, reason: "ok"},
		{name: "truncated but model seen", body: `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}` + "\n\n", status: 200, reason: "response_incomplete"},
		{name: "client canceled", err: context.Canceled, reason: "transport_or_read_error"},
		{name: "read error", err: errors.New("connection reset"), reason: "transport_or_read_error"},
		{name: "http error keeps pair", body: `{"status":"completed"}`, status: 503, reason: "http_status"},
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
			resp, reqErr := svc.doOpenAIUpstream(req, "", account)
			if tc.err != nil {
				require.Error(t, reqErr)
			} else {
				require.NoError(t, reqErr)
			}
			if resp != nil {
				_, _ = io.ReadAll(resp.Body)
				require.NoError(t, resp.Body.Close())
			}

			invalidations := ticketLogEvents(sink, "openai_codex_ticket invalidated")
			require.Len(t, invalidations, tc.invalidations)
			for _, event := range invalidations {
				require.Equal(t, tc.reason, event.Fields["reason"])
			}
			gens := ticketLogEvents(sink, "openai_codex_ticket generation")
			require.Len(t, gens, 1)
			require.Equal(t, tc.reason, gens[0].Fields["reason"])
			stored := svc.lookupOpenAICodexTicket(account, ticket.Model)
			require.Equal(t, !tc.drop, stored.valid(time.Now(), 292))
			if tc.drop {
				require.True(t, svc.ticketProbeCoolingDown(account.ID, ticket.Model, time.Now()) == false, "cooldown must be cleared so the harvester re-mints")
			}
		})
	}
}

func TestCodexTicketWebSocketTerminalWithoutModelInvalidates(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	account := ticketTestAccount(41)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://proxy.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.storeOpenAICodexTicket(context.Background(), account, ticket)

	// Whole-turn observer saw a matching model earlier: silence on the terminal
	// frame does not kill the pair, matching fenjue's "model found" behavior.
	models := &upstreamResponseModelObserver{}
	models.ObserveOpenAI([]byte(`{"type":"response.created","response":{"model":"gpt-6-astra"}}`), "response.created")
	svc.observeOpenAICodexTicketWS(account, ticket, []byte(`{"type":"response.completed"}`), models)
	require.Len(t, ticketLogEvents(sink, "openai_codex_ticket invalidated"), 0)

	// No model ever observed in the turn: the terminal frame is the signal.
	empty := &upstreamResponseModelObserver{}
	svc.observeOpenAICodexTicketWS(account, ticket, []byte(`{"type":"response.failed"}`), empty)
	invalidations := ticketLogEvents(sink, "openai_codex_ticket invalidated")
	require.Len(t, invalidations, 1)
	require.Equal(t, "response_failed", invalidations[0].Fields["reason"])
	require.False(t, svc.lookupOpenAICodexTicket(account, ticket.Model).valid(time.Now(), 292))
}
