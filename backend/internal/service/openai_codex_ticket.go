package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix = "codex_turn_ticket:"
	// OpenAICodexTicketAccountEnabledExtraKey is a tri-state account override:
	// absent inherits global/group policy, true forces on, false forces off.
	OpenAICodexTicketAccountEnabledExtraKey = "openai_codex_ticket_enabled"
	openAICodexAstraMinVersion              = "0.155.0"
	openAICodexTicketStatePrefix            = "gAAAAA"
	openAICodexTicketDefaultModel           = "gpt-6-astra"
	openAICodexTicketDefaultSolModel        = "gpt-5.6-sol"
	openAICodexTicketPersonalBlocks         = 10
	openAICodexTicketTeamBlocks             = 12
)

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有可用的 292 门票，
// 且 fail_closed 禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID     int64     `json:"account_id"`
	Model         string    `json:"model"`
	State         string    `json:"state"`
	Length        int       `json:"length"`
	CapturedAt    time.Time `json:"captured_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Attempts      int       `json:"attempts"`
	Cookie        string    `json:"cookie"`
	ProxyURL      string    `json:"proxy_url"`
	ResponseModel string    `json:"response_model"`
	Blocks        int       `json:"blocks,omitempty"`
	IssuedAt      time.Time `json:"issued_at,omitempty"`
}

type openAICodexTicketShape struct {
	Blocks   int
	IssuedAt time.Time
}

func parseOpenAICodexTicketShape(value string) (openAICodexTicketShape, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\t ") {
		return openAICodexTicketShape{}, errors.New("invalid state encoding")
	}
	core := strings.TrimRight(value, "=")
	if len(value)-len(core) > 2 {
		return openAICodexTicketShape{}, errors.New("invalid state padding")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(core)
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return openAICodexTicketShape{}, errors.New("unrecognized state envelope")
	}
	issuedUnix := binary.BigEndian.Uint64(raw[1:9])
	if issuedUnix < 1577836800 || issuedUnix >= 4102444800 {
		return openAICodexTicketShape{}, errors.New("state timestamp out of range")
	}
	return openAICodexTicketShape{Blocks: (len(raw) - 57) / 16, IssuedAt: time.Unix(int64(issuedUnix), 0)}, nil
}

// openAICodexTicketTeamPlanMarkers identify ChatGPT Team/Business/Enterprise
// subscriptions. Upstream does not report a single canonical plan_type: besides
// "team" it also emits variants such as "self_serve_business_prolite", so these
// plans are detected by substring instead of equality.
var openAICodexTicketTeamPlanMarkers = []string{"team", "business", "enterprise"}

func openAICodexTicketIsTeamPlan(account *Account) bool {
	if account == nil {
		return false
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	if plan == "" {
		return false
	}
	for _, marker := range openAICodexTicketTeamPlanMarkers {
		if strings.Contains(plan, marker) {
			return true
		}
	}
	return false
}

func openAICodexTicketExpectedBlocks(account *Account) int {
	if openAICodexTicketIsTeamPlan(account) {
		return openAICodexTicketTeamBlocks
	}
	return openAICodexTicketPersonalBlocks
}

func openAICodexTicketExpectedLength(account *Account) int {
	return base64.URLEncoding.EncodedLen(57 + 16*openAICodexTicketExpectedBlocks(account))
}

func openAICodexTicketTargetLength(account *Account, cfg config.OpenAICodexTicketConfig) int {
	if cfg.TargetLength > 0 && cfg.TargetLength != 292 {
		return cfg.TargetLength
	}
	return openAICodexTicketExpectedLength(account)
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

// OpenAICodexTicketAccountOverride returns the explicit per-account override.
// The second result is false when the account inherits global/group policy.
func OpenAICodexTicketAccountOverride(account *Account) (bool, bool) {
	if account == nil || account.Extra == nil {
		return false, false
	}
	value, ok := account.Extra[OpenAICodexTicketAccountEnabledExtraKey]
	if !ok {
		return false, false
	}
	enabled, ok := value.(bool)
	if !ok {
		return false, false
	}
	return enabled, true
}

// accountTicketOverrideEnabled reports an explicit account-level force-on.
// Such accounts are harvested even outside the configured harvest scope;
// scope.allowsAccount still applies operational scheduling state.
func accountTicketOverrideEnabled(account *Account) bool {
	enabled, overridden := OpenAICodexTicketAccountOverride(account)
	return overridden && enabled
}

func openAICodexTicketProxyLogValue(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "none"
	}
	if ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return "invalid"
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return "configured"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func openAICodexTicketBundleID(ticket *openAICodexTicket) string {
	if ticket == nil {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(ticket.State))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(ticket.Cookie))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(ticket.ProxyURL))
	sum := hash.Sum(nil)
	return fmt.Sprintf("%x", sum[:8])
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

// NormalizeOpenAICodexTicketModels removes empty/duplicate model names while
// preserving the configured order. An empty result is meaningful: it disables
// ticket gating for every model while leaving the global feature enabled.
func NormalizeOpenAICodexTicketModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds < 30 {
		cfg.HarvestProbeIntervalSeconds = 180
	}
	if cfg.HarvestCooldownSeconds <= 0 {
		cfg.HarvestCooldownSeconds = 180
	}
	if cfg.MaxProbesPerRound <= 0 {
		cfg.MaxProbesPerRound = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if s != nil && s.settingService != nil {
		cfg.Models = s.settingService.GetOpenAICodexTicketModels(context.Background(), cfg.Models)
		cfg.FailClosed = s.settingService.GetOpenAICodexTicketFailClosed(context.Background())
	}
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string     `json:"model"`
	Length           int        `json:"length,omitempty"`
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Blocked          bool       `json:"blocked"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	accountEnabled, accountOverride := OpenAICodexTicketAccountOverride(account)
	if (!cfg.Enabled && !(accountOverride && accountEnabled)) || (accountOverride && !accountEnabled) || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	models, targetLen := cfg.Models, openAICodexTicketTargetLength(account, cfg)
	if models == nil {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if targetLen <= 0 {
		targetLen = 292
	}
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		status := OpenAICodexTicketStatus{Model: model}
		ticket := parseOpenAICodexTicketFromAny(0, model, nil)
		if account != nil && account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket.valid(now, targetLen) {
			status.Ready = true
			status.Length = ticket.Length
			remaining := int64(ticket.ExpiresAt.Sub(now) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
			exp := ticket.ExpiresAt
			status.ExpiresAt = &exp
		}
		status.Blocked = cfg.FailClosed && !status.Ready
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledForAccount(ctx context.Context, account *Account) bool {
	if enabled, overridden := OpenAICodexTicketAccountOverride(account); overridden {
		return enabled
	}
	return s.openAICodexTicketEnabledContext(ctx)
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModelForAccount(ctx context.Context, account *Account, model string) bool {
	return s.openAICodexTicketEnabledForAccount(ctx, account) && s.openAICodexTicketGatedModel(model)
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (t *openAICodexTicket) valid(now time.Time, targetLen int) bool {
	if t == nil {
		return false
	}
	state := strings.TrimSpace(t.State)
	if (targetLen != 292 && targetLen != 332) || (len(state) != 292 && len(state) != 332) || t.Length != len(state) || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	if !validOpenAICodexTicketCookie(t.Cookie) || t.ProxyURL == "" || ValidateOpenAICodexTicketHarvestProxyURL(t.ProxyURL) != nil || t.ResponseModel != t.Model {
		return false
	}
	shape, err := parseOpenAICodexTicketShape(state)
	if err != nil || shape.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(shape.IssuedAt.Add(time.Hour-30*time.Second)) {
		return false
	}
	if t.ExpiresAt.IsZero() || !now.Before(t.ExpiresAt) {
		return false
	}
	if !t.IssuedAt.IsZero() && (t.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(t.IssuedAt.Add(time.Hour-30*time.Second))) {
		return false
	}
	return true
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	var extra *openAICodexTicket
	if account.Extra != nil {
		extra = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	for {
		raw, exists := s.openaiCodexTickets.Load(key)
		mem, _ := raw.(*openAICodexTicket)
		// Newer tombstones prevent stale scheduler snapshots reviving rejected pairs.
		if extra == nil || (mem != nil && !extra.CapturedAt.After(mem.CapturedAt)) {
			return mem
		}
		if !exists {
			if _, loaded := s.openaiCodexTickets.LoadOrStore(key, extra); !loaded {
				return extra
			}
		} else if s.openaiCodexTickets.CompareAndSwap(key, raw, extra) {
			return extra
		}
	}
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return
	}
	s.openaiCodexTicketMutationMu.Lock()
	defer s.openaiCodexTicketMutationMu.Unlock()
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Model = model
	ticket.AccountID = account.ID
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	if s.accountRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		logger.L().Warn("openai_codex_ticket persist failed",
			zap.Int64("account_id", account.ID),
			zap.String("model", model),
			zap.String("bundle_id", openAICodexTicketBundleID(ticket)),
			zap.String("error_class", codexTicketErrorClass(err)),
		)
	}
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledForAccount(ctx, account) {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModelForAccount(ctx, account, model) {
		return nil
	}
	cfg := s.openAICodexTicketConfig()
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
		setOpenAICodexTicketHeaders(h, ticket)
		logger.L().Debug("openai_codex_ticket applied",
			zap.Int64("account_id", account.ID),
			zap.String("model", model),
			zap.String("bundle_id", openAICodexTicketBundleID(ticket)),
			zap.Int("length", ticket.Length),
			zap.Bool("cookie_present", ticket.Cookie != ""),
			zap.String("proxy_endpoint", openAICodexTicketProxyLogValue(ticket.ProxyURL)),
		)
		return nil
	}
	if !cfg.FailClosed {
		return nil
	}
	return ErrOpenAICodexTicketUnavailable
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledForAccount(context.Background(), account) {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if !cfg.FailClosed {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketGatedModelForAccount(context.Background(), account, model) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg))
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (*openAICodexTicket, int, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	body := openAICodexTicketProbeBody(model)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest)))
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return nil, 0, errors.New("probe account identity unavailable")
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	// A mint never sends cookies or a previous ticket, including through plugins.
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, 0, fmt.Errorf("ticket probe transport failed: %s", codexTicketErrorClass(err))
	}
	if resp == nil {
		return nil, 0, errors.New("nil upstream response")
	}
	state := extractOpenAICodexTurnState(resp.Header)
	ticket := &openAICodexTicket{Model: model, State: state, Length: len(state), Cookie: openAICodexTicketCookie(resp.Cookies()), ProxyURL: proxyURL}
	if resp.Body == nil {
		return ticket, resp.StatusCode, errors.New("empty probe response")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return ticket, resp.StatusCode, errors.New("probe response incomplete")
	}
	responseModel, err := openAICodexTicketProbeModel(raw)
	ticket.ResponseModel = responseModel
	return ticket, resp.StatusCode, err
}

func (s *OpenAIGatewayService) ticketProbeCoolingDown(accountID int64, model string, now time.Time) bool {
	if s == nil {
		return false
	}
	key := openAICodexTicketKey(accountID, model)
	value, _ := s.openaiCodexTicketProbeCooldown.Load(key)
	until, ok := value.(time.Time)
	if !ok || !until.After(now) {
		s.openaiCodexTicketProbeCooldown.Delete(key)
		return false
	}
	return true
}

func (s *OpenAIGatewayService) cooldownTicketProbe(accountID int64, model string, cfg config.OpenAICodexTicketConfig) {
	if s == nil {
		return
	}
	s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(accountID, model), time.Now().Add(time.Duration(cfg.HarvestCooldownSeconds)*time.Second))
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	h.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	h.Set(openAICodexTicketLiteHeader, "true")
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// refreshOpenAICodexTickets probes each account/model with a missing or soon-to-expire
// ticket once. The loop waits for all probes, then waits the configured interval
// before starting the next cycle.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	scope, err := s.settingService.GetCodexTicketHarvestScope(ctx)
	if err != nil {
		logger.L().Warn("openai_codex_ticket harvest scope unavailable; skipping round", zap.Error(err))
		return
	}
	tiers := map[codexHarvestTier][]Account{}
	seen := make(map[int64]bool, len(accounts))
	for _, account := range accounts {
		if seen[account.ID] || !isOpenAICodexTicketAccount(&account) || !s.openAICodexTicketEnabledForAccount(ctx, &account) || (!accountTicketOverrideEnabled(&account) && !scope.includes(&account)) || !scope.allowsAccount(&account) {
			continue
		}
		seen[account.ID] = true
		tier := scope.tier(&account)
		tiers[tier] = append(tiers[tier], account)
	}
	keys := make([]codexHarvestTier, 0, len(tiers))
	for key := range tiers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Schedulable != keys[j].Schedulable {
			return keys[i].Schedulable
		}
		if keys[i].Priority != keys[j].Priority {
			return keys[i].Priority < keys[j].Priority
		}
		return keys[i].AccountPriority < keys[j].AccountPriority
	})
	s.openaiCodexTicketCursors.Range(func(key, _ any) bool {
		tier, ok := key.(codexHarvestTier)
		if !ok {
			s.openaiCodexTicketCursors.Delete(key)
			return true
		}
		if _, exists := tiers[tier]; !exists {
			s.openaiCodexTicketCursors.Delete(key)
		}
		return true
	})
	counts := [2]int{}
	probed := 0
	for _, tier := range keys {
		pool := tiers[tier]
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		if ctx.Err() != nil || probed >= cfg.MaxProbesPerRound {
			break
		}
		total := len(pool) * len(cfg.Models)
		if total == 0 {
			continue
		}
		stored, _ := s.openaiCodexTicketCursors.LoadOrStore(tier, &atomic.Uint64{})
		cursor, ok := stored.(*atomic.Uint64)
		if !ok || cursor == nil {
			cursor = &atomic.Uint64{}
			s.openaiCodexTicketCursors.Store(tier, cursor)
		}
		start := int(cursor.Load() % uint64(total))
		var wg sync.WaitGroup
		for offset := 0; offset < total && probed < cfg.MaxProbesPerRound && ctx.Err() == nil; offset++ {
			index := (start + offset) % total
			cursor.Store(uint64((index + 1) % total))
			account := pool[index/len(cfg.Models)]
			model := normalizeOpenAICodexTicketModel(cfg.Models[index%len(cfg.Models)])
			if model == "" || s.ticketProbeCoolingDown(account.ID, model, now) {
				continue
			}
			ticket := s.lookupOpenAICodexTicket(&account, model)
			if ticket.valid(now, openAICodexTicketTargetLength(&account, cfg)) && !ticket.needsRefresh(now, refreshBefore) {
				continue
			}
			acc := account
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			if tier.Schedulable {
				counts[0]++
			} else {
				counts[1]++
			}
			wg.Add(1)
			go func(acc Account, model string) {
				defer wg.Done()
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}(acc, model)
		}
		wg.Wait()
	}
	if probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed),
			zap.Int("schedulable_probed", counts[0]), zap.Int("deferred_probed", counts[1]),
			zap.String("scope", scope.Mode), zap.Int("selected_groups", len(scope.GroupIDs)))
	}
}

var (
	// ErrOpenAICodexTicketRefreshUnavailable means no transport owns the ticket
	// harvester, so an on-demand re-probe cannot be served.
	ErrOpenAICodexTicketRefreshUnavailable = infraerrors.ServiceUnavailable(
		"CODEX_TICKET_REFRESH_UNAVAILABLE", "codex ticket harvester is unavailable")
	// ErrOpenAICodexTicketNotApplicable means the account cannot hold tickets
	// or inherits a disabled feature.
	ErrOpenAICodexTicketNotApplicable = infraerrors.BadRequest(
		"CODEX_TICKET_NOT_APPLICABLE", "codex tickets are not enabled for this account")
	// ErrOpenAICodexTicketModelNotGated means none of the requested models is
	// in the ticket-gated model list.
	ErrOpenAICodexTicketModelNotGated = infraerrors.BadRequest(
		"CODEX_TICKET_MODEL_NOT_GATED", "none of the requested models is ticket-gated")
)

// RefreshOpenAICodexTicketsForAccount re-probes the account's gated models on
// demand. Explicit admin intent bypasses the harvest scope and existing probe
// cooldowns, but the probe itself reuses probeOnceOpenAICodexTicket unchanged
// (singleflight, cooldown re-arm on miss, full validity checks). Returns the
// resulting per-model statuses; a miss simply shows up as not ready.
func (s *OpenAIGatewayService) RefreshOpenAICodexTicketsForAccount(ctx context.Context, accountID int64, models []string) ([]OpenAICodexTicketStatus, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrOpenAICodexTicketRefreshUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledForAccount(ctx, account) {
		return nil, ErrOpenAICodexTicketNotApplicable
	}
	cfg := s.openAICodexTicketConfig()
	gated := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		if model = normalizeOpenAICodexTicketModel(model); model != "" {
			gated = append(gated, model)
		}
	}
	targets := gated
	if requested := NormalizeOpenAICodexTicketModels(models); len(requested) > 0 {
		targets = targets[:0]
		for _, model := range requested {
			if slices.Contains(gated, model) {
				targets = append(targets, model)
			}
		}
	}
	if len(targets) == 0 {
		return nil, ErrOpenAICodexTicketModelNotGated
	}
	acc := *account
	acc.Extra = maps.Clone(account.Extra)
	acc.Credentials = maps.Clone(account.Credentials)
	var wg sync.WaitGroup
	for _, model := range targets {
		s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, model))
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			s.probeOnceOpenAICodexTicket(ctx, &acc, model)
		}(model)
	}
	wg.Wait()
	// Statuses read account.Extra; probe results live in the in-process store
	// until the next snapshot. Merge both so the response reflects this refresh.
	display := *account
	display.Extra = maps.Clone(account.Extra)
	if display.Extra == nil {
		display.Extra = make(map[string]any, len(gated))
	}
	for _, model := range gated {
		if current := s.lookupOpenAICodexTicket(account, model); current != nil {
			display.Extra[openAICodexTicketExtraKey(model)] = current
		}
	}
	return OpenAICodexTicketStatuses(&display, cfg, time.Now()), nil
}

// probeOnceOpenAICodexTicket saves only a complete ticket/cookie/egress bundle
// whose successful response declares the requested model. Misses cool down.
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if s == nil || !isOpenAICodexTicketAccount(account) || ctx.Err() != nil || !s.openAICodexTicketEnabledForAccount(ctx, account) {
		return
	}
	cfg := s.openAICodexTicketConfig()
	proxyURL := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if proxyURL == "" || s.httpUpstream == nil || ctx.Err() != nil {
		if ctx.Err() == nil {
			reason := "missing_proxy"
			if s.httpUpstream == nil {
				reason = "missing_transport"
			}
			logger.L().Warn("openai_codex_ticket probe skipped", zap.Int64("account_id", account.ID), zap.String("model", model), zap.String("reason", reason))
		}
		return
	}
	if s.ticketProbeCoolingDown(account.ID, model, time.Now()) {
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	_, _, _ = s.openaiCodexTicketFlight.Do(key, func() (any, error) {
		started := time.Now()
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			s.cooldownTicketProbe(account.ID, model, cfg)
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "token"), zap.String("error_class", codexTicketErrorClass(err)),
				zap.Int("cooldown_seconds", cfg.HarvestCooldownSeconds))
			return nil, nil
		}
		ticket, status, perr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
		if perr != nil {
			s.cooldownTicketProbe(account.ID, model, cfg)
			if ticket == nil {
				ticket = &openAICodexTicket{Model: model, ProxyURL: proxyURL}
			}
			reason := "probe_error"
			if status != 0 && status != http.StatusOK {
				reason = "http_status"
			}
			logger.L().Info("openai_codex_ticket probe miss", append(codexTicketLogFields(account, ticket),
				zap.String("reason", reason), zap.Int("http", status), zap.String("response_model", ticket.ResponseModel),
				zap.Int("cooldown_seconds", cfg.HarvestCooldownSeconds), zap.Int64("duration_ms", time.Since(started).Milliseconds()), zap.Error(perr))...)
			return nil, nil
		}
		state := ticket.State
		shape, shapeErr := parseOpenAICodexTicketShape(state)
		expectedLength := openAICodexTicketTargetLength(account, cfg)
		now := time.Now()
		ticket.CapturedAt = now
		ticket.ExpiresAt = now.Add(time.Duration(cfg.TTLSeconds) * time.Second)
		if bound := shape.IssuedAt.Add(time.Hour - 30*time.Second); bound.Before(ticket.ExpiresAt) {
			ticket.ExpiresAt = bound
		}
		if status != http.StatusOK || shapeErr != nil || !ticket.valid(now, expectedLength) {
			s.cooldownTicketProbe(account.ID, model, cfg)
			reason := "invalid_ticket"
			switch {
			case status != http.StatusOK:
				reason = "http_status"
			case shapeErr != nil:
				reason = "state_shape"
			case len(ticket.Cookie) == 0:
				reason = "missing_cookie"
			case !validOpenAICodexTicketCookie(ticket.Cookie):
				reason = "invalid_cookie"
			case ticket.ResponseModel != model:
				reason = "response_model_mismatch"
			case len(state) != 292 && len(state) != 332:
				reason = "ticket_length"
			}
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", reason), zap.Int("http", status), zap.Int("length", len(state)),
				zap.Int("cooldown_seconds", cfg.HarvestCooldownSeconds), zap.Int64("duration_ms", time.Since(started).Milliseconds()),
				zap.Int("expected_length", expectedLength), zap.Int("blocks", shape.Blocks),
				zap.String("response_model", ticket.ResponseModel), zap.Bool("cookie_present", ticket.Cookie != ""),
				zap.String("proxy_endpoint", openAICodexTicketProxyLogValue(proxyURL)), zap.Error(shapeErr))
			return nil, nil
		}
		s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, model))
		ticket.AccountID = account.ID
		ticket.Attempts = 1
		ticket.Blocks = shape.Blocks
		ticket.IssuedAt = shape.IssuedAt
		s.storeOpenAICodexTicket(ctx, account, ticket)
		logger.L().Info("openai_codex_ticket harvested",
			zap.Int64("account_id", account.ID), zap.String("model", model),
			zap.Int("length", ticket.Length), zap.Int("blocks", ticket.Blocks),
			zap.Int("http", status), zap.Int64("duration_ms", time.Since(started).Milliseconds()),
			zap.Time("expires_at", ticket.ExpiresAt),
			zap.String("response_model", ticket.ResponseModel), zap.Bool("cookie_present", ticket.Cookie != ""),
			zap.String("proxy_endpoint", openAICodexTicketProxyLogValue(ticket.ProxyURL)),
			zap.String("bundle_id", openAICodexTicketBundleID(ticket)), zap.String("mode", "continuous"))
		return nil, nil
	})
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
