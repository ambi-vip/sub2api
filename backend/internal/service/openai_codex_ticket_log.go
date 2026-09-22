package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Log only routing metadata. Never serialize tickets, headers, request bodies,
// upstream errors or repository errors (which can include persisted secrets).
func codexTicketLogFields(account *Account, ticket *openAICodexTicket) []zap.Field {
	fields := []zap.Field{zap.String("component", "openai_codex_ticket")}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	if ticket != nil {
		fields = append(fields, zap.String("model", ticket.Model),
			zap.String("bundle_id", openAICodexTicketBundleID(ticket)),
			zap.Int("length", ticket.Length), zap.Bool("cookie_present", ticket.Cookie != ""),
			zap.String("proxy_endpoint", openAICodexTicketProxyLogValue(ticket.ProxyURL)))
	}
	return fields
}

func codexTicketErrorClass(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return "timeout"
	}
	return "error"
}

func logCodexTicketDecision(account *Account, ticket *openAICodexTicket, model, transport, reason string, failClosed bool) {
	fields := codexTicketLogFields(account, ticket)
	if ticket == nil {
		fields = append(fields, zap.String("model", model))
	}
	fields = append(fields, zap.String("transport", transport), zap.String("reason", reason), zap.Bool("fail_closed", failClosed))
	logger.L().Info("openai_codex_ticket request decision", fields...)
}

// One summary per HTTP response, including streams closed before EOF. The
// wrapper passes through bytes unchanged and does not buffer the full response.
type codexTicketGenerationLog struct {
	mu                sync.Mutex
	done              bool
	account           *Account
	ticket            *openAICodexTicket
	transport         string
	status            int
	started           time.Time
	models            upstreamResponseModelObserver
	completed, failed bool
	pluginHandled     bool
	// drop receives the failure reason when the generation ended in a way
	// fenjue treats as a dead pair: a different model, or a cleanly
	// terminated stream that never declared one.
	drop func(reason string)
}

func (o *codexTicketGenerationLog) observe(payload []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	event := gjson.GetBytes(payload, "type").String()
	status := firstValidTrimmedGJSONString(payload, "response.status", "status")
	o.models.ObserveOpenAI(payload, event)
	o.completed = o.completed || event == "response.completed" || status == "completed"
	o.failed = o.failed || event == "error" || event == "response.failed" || event == "response.incomplete" || status == "failed" || status == "incomplete"
}

func (o *codexTicketGenerationLog) finish(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done || o.ticket == nil {
		return
	}
	o.done = true
	reason := "ok"
	switch {
	case err != nil && err != io.EOF:
		reason = "transport_or_read_error"
	case o.transport == "http" && o.status != 200:
		reason = "http_status"
	case o.models.Conflict() || (o.models.Model() != "" && o.models.Model() != o.ticket.Model):
		reason = "response_model_mismatch"
	case o.failed:
		reason = "response_failed"
	case !o.completed:
		reason = "response_incomplete"
	case o.models.Model() == "":
		reason = "missing_response_model"
	}
	// fenjue semantics: any non-matching observed model (including silence on
	// a stream the upstream finished speaking) kills the pair. Client-driven
	// aborts, timeouts and non-200 replies carry no model signal and keep it.
	if o.drop != nil && o.upstreamEndedClean(err) && (reason == "response_model_mismatch" || o.models.Model() == "") {
		o.drop(reason)
	}
	fields := append(codexTicketLogFields(o.account, o.ticket),
		zap.String("transport", o.transport),
		zap.String("response_model", o.models.Model()), zap.Bool("model_conflict", o.models.Conflict()),
		zap.Bool("completed", o.completed), zap.String("reason", reason),
		zap.String("error_class", codexTicketErrorClass(err)))
	if o.transport == "http" {
		fields = append(fields, zap.Int("http", o.status), zap.Int64("duration_ms", time.Since(o.started).Milliseconds()), zap.Bool("plugin_handled", o.pluginHandled))
	}
	if reason == "ok" {
		logger.L().Info("openai_codex_ticket generation", fields...)
	} else {
		logger.L().Warn("openai_codex_ticket generation", fields...)
	}
}

// upstreamEndedClean reports whether the upstream finished speaking on its own
// terms: an HTTP 200 body read to completion, or a WS terminal frame. Only
// then does a missing model say something about the pair; canceled requests,
// timeouts and read errors do not.
func (o *codexTicketGenerationLog) upstreamEndedClean(err error) bool {
	class := codexTicketErrorClass(err)
	if class == "canceled" || class == "timeout" {
		return false
	}
	if o.transport == "websocket" {
		return true
	}
	return o.status == http.StatusOK && (err == nil || errors.Is(err, io.EOF))
}

// WS callers supply complete event frames. Emit summaries only on terminal
// events; per-delta invalidation checks do not flood the log.
func (s *OpenAIGatewayService) observeOpenAICodexTicketWS(account *Account, ticket *openAICodexTicket, payload []byte, models *upstreamResponseModelObserver) {
	if ticket == nil {
		return
	}
	s.invalidateOpenAICodexTicket(account, ticket, payload)
	event := gjson.GetBytes(payload, "type").String()
	if event != "response.completed" && event != "response.failed" && event != "response.incomplete" && event != "error" {
		return
	}
	o := &codexTicketGenerationLog{account: account, ticket: ticket, transport: "websocket",
		drop: func(reason string) { s.invalidateOpenAICodexTicketSignal(account, ticket, reason, "") }}
	o.observe(payload)
	if models != nil {
		o.models = *models
	}
	o.finish(nil)
}
