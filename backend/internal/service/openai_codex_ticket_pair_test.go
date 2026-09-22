package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func ticketPairTestHeaders() http.Header {
	return http.Header{"Set-Cookie": []string{"__cflb=c; Path=/; Secure", "__oailb=o; Path=/; HttpOnly"}}
}
func ticketPairTestResponse(model string) string {
	return `data: {"type": "response.completed", "response":{"id":"resp_ticket","status":"completed","usage":{"input_tokens":1,"output_tokens":1},"model":` + jsonString(model) + `}}` + "\n\n"
}

func TestCodexTicketPairMintValidation(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		length         int
		cookies        []string
		good           bool
	}{
		{"personal", ticketPairTestResponse("gpt-6-astra"), 292, ticketPairTestHeaders().Values("Set-Cookie"), true},
		{"team without plan metadata", ticketPairTestResponse("gpt-6-astra"), 332, ticketPairTestHeaders().Values("Set-Cookie"), true},
		{"downgrade", ticketPairTestResponse("gpt-5.6-luna"), 292, ticketPairTestHeaders().Values("Set-Cookie"), false},
		{"wrong shape", ticketPairTestResponse("gpt-6-astra"), 312, ticketPairTestHeaders().Values("Set-Cookie"), false},
		{"missing cookie", ticketPairTestResponse("gpt-6-astra"), 292, []string{"__cflb=c"}, false},
		{"deleted cookie", ticketPairTestResponse("gpt-6-astra"), 292, []string{"__cflb=c", "__oailb=o; Max-Age=0"}, false},
		{"duplicate cookie", ticketPairTestResponse("gpt-6-astra"), 292, []string{"__cflb=c", "__oailb=o", "__oailb=other"}, false},
		{"truncated", "data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n", 292, ticketPairTestHeaders().Values("Set-Cookie"), false},
		{"spaced error", "data: {\"type\": \"response.failed\"}\n\n", 292, ticketPairTestHeaders().Values("Set-Cookie"), false},
		{"conflicting model", ticketPairTestResponse("gpt-5.6-luna") + ticketPairTestResponse("gpt-6-astra"), 292, ticketPairTestHeaders().Values("Set-Cookie"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				require.Empty(t, req.Header.Get("Cookie"))
				require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
				require.Equal(t, "true", req.Header.Get(openAICodexTicketLiteHeader))
				require.Equal(t, "responses_websockets=2026-02-06", req.Header.Get("OpenAI-Beta"))
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, "additional_tools", gjson.GetBytes(body, "input.0.type").String())
				require.Equal(t, "all_turns", gjson.GetBytes(body, "reasoning.context").String())
				require.Equal(t, "false", gjson.GetBytes(body, "parallel_tool_calls").Raw)
				h := http.Header{"Set-Cookie": tc.cookies}
				h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(tc.length))
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://mint.example:8080"}, upstream)
			account := ticketTestAccount(41)
			svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
			ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
			require.Equal(t, tc.good, ticket.valid(time.Now(), 292))
			if tc.good {
				require.Equal(t, "__cflb=c; __oailb=o", ticket.Cookie)
				require.LessOrEqual(t, ticket.ExpiresAt.Unix(), ticket.IssuedAt.Add(time.Hour-30*time.Second).Unix())
			}
		})
	}
}

func TestCodexTicketPairRequestKeepsSelectedBundle(t *testing.T) {
	ctx := context.Background()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(ticketPairTestResponse("gpt-6-astra")))}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	account := ticketTestAccount(41)
	old := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=old; __oailb=old", ProxyURL: "http://old.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc.storeOpenAICodexTicket(ctx, account, old)
	req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, svc.applyOpenAICodexTicketRequest(ctx, account, old.Model, req))
	next := *old
	next.CapturedAt = time.Now()
	next.Cookie = "__cflb=new; __oailb=new"
	next.ProxyURL = "http://new.example:8080"
	svc.storeOpenAICodexTicket(ctx, account, &next)
	resp, err := svc.doOpenAIUpstream(req, "http://account.example:8080", account)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, old.ProxyURL, upstream.lastProxyURL)
	require.Equal(t, old.Cookie, upstream.requests[0].Header.Get("Cookie"))
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(upstream.requests[0].Context()))
	require.False(t, svc.openAICodexTicketSessionUsable(account, old, old.Model))
	require.True(t, svc.openAICodexTicketSessionUsable(account, &next, next.Model))
	require.False(t, svc.openAICodexTicketSessionUsable(account, &next, "gpt-5.6-sol"))
	svc.invalidateOpenAICodexTicket(account, &next, []byte(`{"model":"gpt-5.6-luna"}`))
	require.NoError(t, svc.applyOpenAICodexTicketRequest(ctx, account, next.Model, req))
	require.Empty(t, req.Header.Get("Cookie"))
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
	require.Nil(t, req.Context().Value(openAICodexTicketRequestKey{}).(*openAICodexTicket))
}

func TestCodexTicketPairSessionUsableAcceptsTeamTicketLength(t *testing.T) {
	ctx := context.Background()
	account := ticketTestAccount(41)
	account.Credentials["plan_type"] = "team"
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292}, nil)
	ticket := &openAICodexTicket{
		Model: "gpt-6-astra", State: fakeCodexTicketState(332), Length: 332,
		Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://mint.example:8080", ResponseModel: "gpt-6-astra",
		CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}
	svc.storeOpenAICodexTicket(ctx, account, ticket)
	require.True(t, svc.openAICodexTicketSessionUsable(account, ticket, ticket.Model))
}

func TestCodexTicketPairObserverPreservesChunkedStream(t *testing.T) {
	raw := ": keepalive\n" + ticketPairTestResponse("gpt-6-astra") + strings.Repeat("x", (1<<20)+20) + "\n" + ticketPairTestResponse("gpt-5.6-luna")
	var models []string
	body := &openAICodexTicketResponseBody{ReadCloser: io.NopCloser(strings.NewReader(raw)), observe: func(payload []byte) { models = append(models, gjson.GetBytes(payload, "response.model").String()) }}
	var out strings.Builder
	buf := make([]byte, 7)
	for {
		n, err := body.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.Equal(t, raw, out.String())
	require.Equal(t, []string{"gpt-6-astra", "gpt-5.6-luna"}, models)
}

func TestCodexTicketPairTransportAndInvalidation(t *testing.T) {
	ctx := context.Background()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(ticketPairTestResponse("gpt-5.6-luna")))}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, upstream)
	account := ticketTestAccount(41)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://mint.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc.storeOpenAICodexTicket(ctx, account, ticket)
	account.Extra = map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}
	req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	req.Header.Set("Cookie", "unrelated=secret; __cflb=old")
	require.NoError(t, svc.applyOpenAICodexTicketRequest(ctx, account, ticket.Model, req))
	resp, err := svc.doOpenAIUpstream(req, "http://residential.example:8080", account)
	require.NoError(t, err)
	require.Equal(t, ticket.ProxyURL, upstream.lastProxyURL)
	require.Equal(t, ticket.Cookie, upstream.requests[0].Header.Get("Cookie"))
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, ticketPairTestResponse("gpt-5.6-luna"), string(raw))
	require.True(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model), "stale Extra must not revive an invalidated pair")
	replacement := *ticket
	replacement.CapturedAt = time.Now()
	replacement.Cookie = "__cflb=new; __oailb=new"
	svc.storeOpenAICodexTicket(ctx, account, &replacement)
	svc.invalidateOpenAICodexTicket(account, ticket, []byte(`{"model":"gpt-5.6-luna"}`))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model), "late mismatch must not invalidate replacement")
}

func TestCodexTicketPairLegacyAndWSIsolation(t *testing.T) {
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Minute)}
	require.False(t, ticket.valid(time.Now(), 292), "legacy state-only tickets require reminting")
	ticket.Cookie = "__cflb=c; __oailb=o"
	ticket.ProxyURL = "http://mint.example:8080"
	ticket.ResponseModel = ticket.Model
	h := http.Header{}
	setOpenAICodexTicketHeaders(h, ticket)
	first := normalizeOpenAIWSHandshakeCompatibility(ticketTestAccount(41), h)
	h.Set("Cookie", "__cflb=new; __oailb=new")
	require.NotEqual(t, first, normalizeOpenAIWSHandshakeCompatibility(ticketTestAccount(41), h))
	require.Equal(t, ticket.ProxyURL, openAICodexTicketProxy(ticket, ticketTestAccount(41)))
}

func TestCodexTicketPairNormalizesBusinessTools(t *testing.T) {
	ctx := context.Background()
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://mint.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc.storeOpenAICodexTicket(ctx, account, ticket)
	raw := `{"model":"gpt-6-astra","instructions":"keep user instructions","input":"hello","parallel_tool_calls":true,"tools":[{"type":"namespace","name":"local","tools":[{"type":"function","name":"inspect","parameters":{"type":"object"}}]}],"reasoning":{"effort":"high"}}`
	req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(raw))
	require.NoError(t, svc.applyOpenAICodexTicketRequest(ctx, account, ticket.Model, req))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "keep user instructions", gjson.GetBytes(body, "instructions").String())
	require.Equal(t, "local", gjson.GetBytes(body, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "false", gjson.GetBytes(body, "parallel_tool_calls").Raw)
	require.Equal(t, "all_turns", gjson.GetBytes(body, "reasoning.context").String())
	require.Equal(t, "high", gjson.GetBytes(body, "reasoning.effort").String())
	require.Equal(t, int64(len(body)), req.ContentLength)
	replay, err := req.GetBody()
	require.NoError(t, err)
	defer replay.Close()
	again, err := io.ReadAll(replay)
	require.NoError(t, err)
	require.Equal(t, body, again)
}

func TestCodexTicketPairForwardWithoutClientLiteHeader(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "passthrough"}[passthrough], func(t *testing.T) {
			ctx := context.Background()
			account := ticketTestAccount(41)
			account.Extra = map[string]any{"openai_passthrough": passthrough}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(ticketPairTestResponse("gpt-6-astra")))}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
			ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://mint.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			svc.storeOpenAICodexTicket(ctx, account, ticket)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.155.0")
			raw := []byte(`{"model":"gpt-6-astra","stream":true,"instructions":"test","input":"hello","tools":[{"type":"namespace","name":"local","tools":[{"type":"function","name":"inspect","parameters":{"type":"object"}}]}]}`)
			_, err := svc.Forward(ctx, c, account, raw)
			require.NoError(t, err)
			require.Equal(t, ticket.ProxyURL, upstream.lastProxyURL)
			require.Equal(t, ticket.Cookie, upstream.lastReq.Header.Get("Cookie"))
			require.Equal(t, "true", upstream.lastReq.Header.Get(openAICodexTicketLiteHeader))
			require.Equal(t, "local", gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.0.name`).String())
			require.Equal(t, "false", gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Raw)
		})
	}
}

func TestCodexTicketPairUnsupportedHostedToolFallsBack(t *testing.T) {
	ctx := context.Background()
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://mint.example:8080", ResponseModel: "gpt-6-astra", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	svc.storeOpenAICodexTicket(ctx, account, ticket)
	raw := `{"model":"gpt-6-astra","input":"draw","tools":[{"type":"image_generation"}]}`
	req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(raw))
	require.NoError(t, svc.applyOpenAICodexTicketRequest(ctx, account, ticket.Model, req))
	require.Empty(t, req.Header.Get("Cookie"))
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, raw, string(body))
	headers, resolution, err := svc.buildOpenAIWSHeaders(ctx, nil, account, "token", OpenAIWSProtocolDecision{}, false, "", "", "", ticket.Model, "", []byte(raw))
	require.NoError(t, err)
	require.Nil(t, resolution.Ticket)
	require.Empty(t, headers.Get("Cookie"))
}
