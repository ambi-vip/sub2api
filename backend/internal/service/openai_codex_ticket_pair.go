package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const openAICodexTicketLiteHeader = "X-OpenAI-Internal-Codex-Responses-Lite"

// These are routing cookies, not a shared account cookie jar. Never combine
// cookies from different mint responses or accept unrelated session cookies.
func openAICodexTicketCookie(cookies []*http.Cookie) string {
	values := make(map[string]string, 2)
	for _, cookie := range cookies {
		if cookie.Name != "__cflb" && cookie.Name != "__oailb" {
			continue
		}
		if cookie.Value == "" || cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !time.Now().Before(cookie.Expires)) || cookie.Valid() != nil {
			return ""
		}
		if _, duplicate := values[cookie.Name]; duplicate {
			return ""
		}
		values[cookie.Name] = cookie.Value
	}
	if values["__cflb"] == "" || values["__oailb"] == "" {
		return ""
	}
	return "__cflb=" + values["__cflb"] + "; __oailb=" + values["__oailb"]
}

func validOpenAICodexTicketCookie(value string) bool {
	if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	req := &http.Request{Header: http.Header{"Cookie": []string{value}}}
	cookies := req.Cookies()
	return len(cookies) == 2 && openAICodexTicketCookie(cookies) == value
}

func openAICodexTicketProbeBody(model string) []byte {
	// Match the reference's tool-turn envelope. This synthetic declaration is
	// for minting only; never replace a real user's instructions or tool set.
	return []byte(`{"model":` + jsonString(model) + `,"instructions":"Reply with OK. Do not call tools.","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"codex","description":"local tools","tools":[{"type":"function","name":"noop","description":"Do nothing.","strict":false,"parameters":{"type":"object","properties":{},"additionalProperties":false}}]}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with OK. Do not call tools."}]}],"stream":true,"store":false,"parallel_tool_calls":false,"include":["reasoning.encrypted_content"],"reasoning":{"context":"all_turns"}}`)
}

func openAICodexTicketProbeModel(raw []byte) (string, error) {
	observer := &upstreamResponseModelObserver{}
	completed := false
	observe := func(payload []byte) error {
		if !json.Valid(payload) {
			return errors.New("invalid probe response")
		}
		event := gjson.GetBytes(payload, "type").String()
		status := firstValidTrimmedGJSONString(payload, "response.status", "status")
		if event == "error" || event == "response.failed" || event == "response.incomplete" || status == "failed" || status == "incomplete" || gjson.GetBytes(payload, "error").IsObject() {
			return errors.New("probe response failed")
		}
		observer.ObserveOpenAI(payload, event)
		completed = completed || event == "response.completed" || status == "completed"
		return nil
	}
	if json.Valid(raw) {
		if err := observe(raw); err != nil {
			return "", err
		}
	} else {
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			if err := observe(payload); err != nil {
				return "", err
			}
		}
	}
	if !completed || observer.Model() == "" || observer.Conflict() {
		return observer.Model(), errors.New("probe response missing completion or consistent model")
	}
	return observer.Model(), nil
}

func setOpenAICodexTicketHeaders(h http.Header, ticket *openAICodexTicket) {
	h.Set(openAICodexTurnStateHeader, ticket.State)
	h.Set("Cookie", ticket.Cookie)
	applyOpenAICodexTicketHarvestIdentity(h, ticket.Model)
}

func (s *OpenAIGatewayService) openAICodexTicketForRequest(ctx context.Context, account *Account, model string) (*openAICodexTicket, error) {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) || !s.openAICodexTicketGatedModel(model) {
		return nil, nil
	}
	cfg := s.openAICodexTicketConfig()
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
		return ticket, nil
	}
	if cfg.FailClosed {
		return nil, ErrOpenAICodexTicketUnavailable
	}
	return nil, nil
}

type openAICodexTicketRequestKey struct{}

func (s *OpenAIGatewayService) applyOpenAICodexTicketRequest(ctx context.Context, account *Account, model string, req *http.Request) error {
	if previous, _ := req.Context().Value(openAICodexTicketRequestKey{}).(*openAICodexTicket); previous != nil {
		// Messages and retry builders can apply the policy more than once.
		// A later fail-open decision must remove our earlier injected bundle.
		req.Header.Del(openAICodexTurnStateHeader)
		req.Header.Del("Cookie")
		req.Header.Del(openAICodexTicketLiteHeader)
		*req = *req.WithContext(context.WithValue(req.Context(), openAICodexTicketRequestKey{}, (*openAICodexTicket)(nil)))
	}
	ticket, err := s.openAICodexTicketForRequest(ctx, account, model)
	if err != nil {
		logCodexTicketDecision(account, nil, model, "http", "ticket_unavailable", true)
		return err
	}
	if ticket == nil && isOpenAICodexTicketAccount(account) && s.openAICodexTicketGatedModel(model) {
		logCodexTicketDecision(account, nil, model, "http", "ticket_unavailable", false)
	}
	if ticket != nil {
		if req.Body != nil {
			raw, readErr := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if readErr != nil {
				return readErr
			}
			body, _, normalizeErr := normalizeOpenAIResponsesLitePayloadForAccount(raw, account)
			if normalizeErr != nil {
				req.Body = io.NopCloser(bytes.NewReader(raw))
				logCodexTicketDecision(account, ticket, model, "http", "lite_incompatible", s.openAICodexTicketConfig().FailClosed)
				if !s.openAICodexTicketConfig().FailClosed {
					return nil
				}
				return normalizeErr
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.Header.Del("Content-Length")
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		setOpenAICodexTicketHeaders(req.Header, ticket)
		// Keep the exact pair selected while constructing headers, even if the
		// harvester publishes a newer pair before transport dispatch.
		*req = *req.WithContext(context.WithValue(req.Context(), openAICodexTicketRequestKey{}, ticket))
		logger.L().Debug("openai_codex_ticket applied", append(codexTicketLogFields(account, ticket), zap.String("transport", "http"))...)
	}
	return nil
}

func openAICodexTicketProxy(ticket *openAICodexTicket, account *Account) string {
	if ticket != nil {
		return ticket.ProxyURL
	}
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

func (s *OpenAIGatewayService) openAICodexTicketSessionUsable(account *Account, ticket *openAICodexTicket, model string) bool {
	if ticket == nil {
		return true
	}
	if model != "" && model != ticket.Model {
		return false
	}
	current := s.lookupOpenAICodexTicket(account, ticket.Model)
	return current.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) && current.CapturedAt.Equal(ticket.CapturedAt) && current.State == ticket.State && current.Cookie == ticket.Cookie
}

// Do not replace a new pair when an older in-flight response reports a mismatch.
// Tombstones are persisted under the same mutation lock as mint publication.
func (s *OpenAIGatewayService) invalidateOpenAICodexTicket(account *Account, ticket *openAICodexTicket, payload []byte) {
	if ticket == nil {
		return
	}
	model := firstValidTrimmedGJSONString(payload, "response.model", "model")
	if model == "" || model == ticket.Model {
		return
	}
	s.invalidateOpenAICodexTicketSignal(account, ticket, "response_model_mismatch", model)
}

// invalidateOpenAICodexTicketSignal drops the current pair and lets the
// harvester mint a replacement, mirroring fenjue: reuse stops whenever the
// generation did not declare the ticket's model — a different model, or a
// cleanly terminated stream (completed/failed/incomplete) with no model at
// all. Transport breaks and client cancellations carry no model signal and
// keep the pair.
func (s *OpenAIGatewayService) invalidateOpenAICodexTicketSignal(account *Account, ticket *openAICodexTicket, reason, responseModel string) {
	if s == nil || account == nil || ticket == nil {
		return
	}
	s.openaiCodexTicketMutationMu.Lock()
	defer s.openaiCodexTicketMutationMu.Unlock()
	current := s.lookupOpenAICodexTicket(account, ticket.Model)
	if current == nil || current.State != ticket.State || current.Cookie != ticket.Cookie || !current.CapturedAt.Equal(ticket.CapturedAt) || !current.ExpiresAt.After(time.Now()) {
		return
	}
	invalid := *current
	invalid.ExpiresAt = time.Now()
	invalid.CapturedAt = time.Now()
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, ticket.Model), &invalid)
	// Allow the next background cycle to mint a replacement. No synchronous
	// retries of user requests (which could duplicate tools or bill twice).
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, ticket.Model))
	if s.accountRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{openAICodexTicketExtraKey(ticket.Model): &invalid}); err != nil {
			logger.L().Warn("openai_codex_ticket invalidate persist failed", append(codexTicketLogFields(account, ticket), zap.String("error_class", codexTicketErrorClass(err)))...)
		}
	}
	fields := append(codexTicketLogFields(account, ticket), zap.String("reason", reason))
	if responseModel != "" {
		fields = append(fields, zap.String("response_model", responseModel))
	}
	logger.L().Warn("openai_codex_ticket invalidated", fields...)
}

// Observe without consuming or rewriting the stream; cap retained data even
// for large tool/image output frames. The existing response handler owns Close.
type openAICodexTicketResponseBody struct {
	io.ReadCloser
	pending  []byte
	skipping bool
	observe  func([]byte)
	finish   func(error)
}

func (b *openAICodexTicketResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	for _, c := range p[:n] {
		if c == '\n' {
			if !b.skipping {
				b.line()
			}
			b.pending = b.pending[:0]
			b.skipping = false
		} else if !b.skipping {
			if len(b.pending) >= 1<<20 {
				b.pending = b.pending[:0]
				b.skipping = true
			} else {
				b.pending = append(b.pending, c)
			}
		}
	}
	if err == io.EOF && !b.skipping && len(b.pending) > 0 {
		b.line()
		b.pending = nil
	}
	if err != nil && b.finish != nil {
		b.finish(err)
	}
	return n, err
}

func (b *openAICodexTicketResponseBody) Close() error {
	err := b.ReadCloser.Close()
	if b.finish != nil {
		b.finish(err)
	}
	return err
}

func (b *openAICodexTicketResponseBody) line() {
	payload := bytes.TrimSpace(b.pending)
	payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
	if json.Valid(payload) {
		b.observe(payload)
	}
}
