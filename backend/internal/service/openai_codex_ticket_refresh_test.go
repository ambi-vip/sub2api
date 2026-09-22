package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexRefreshUpstream struct {
	HTTPUpstream
	calls    atomic.Int64
	proxy    string
	lastBody []byte
}

func (u *codexRefreshUpstream) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	u.proxy = proxyURL
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		u.lastBody = body
	}
	model := gjson.GetBytes(u.lastBody, "model").String()
	body := "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"" + model + "\",\"status\":\"completed\"}}\n"
	header := http.Header{}
	header.Set("X-Codex-Turn-State", fakeCodexTicketState(292))
	header.Add("Set-Cookie", "__cflb=c; Path=/")
	header.Add("Set-Cookie", "__oailb=o; Path=/")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

type codexRefreshAccountRepo struct {
	AccountRepository
	accounts []Account
	mu       sync.Mutex
	updates  map[string]any
}

func (r *codexRefreshAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, errors.New("account not found")
}

func (r *codexRefreshAccountRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = make(map[string]any)
	}
	for key, value := range updates {
		r.updates[key] = value
	}
	return nil
}

func codexRefreshService(t *testing.T, cfg config.OpenAICodexTicketConfig, accounts ...Account) (*OpenAIGatewayService, *codexRefreshUpstream, *codexRefreshAccountRepo) {
	t.Helper()
	u := &codexRefreshUpstream{}
	repo := &codexRefreshAccountRepo{accounts: accounts}
	svc := ticketTestService(t, cfg, u)
	svc.accountRepo = repo
	return svc, u, repo
}

func TestRefreshOpenAICodexTicketsForAccount_Success(t *testing.T) {
	account := ticketTestAccount(41)
	svc, u, repo := codexRefreshService(t, config.OpenAICodexTicketConfig{
		Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://harvest.example:8080",
	}, *account)
	// A hot cooldown and a stale persisted ticket must not block a manual refresh.
	svc.cooldownTicketProbe(account.ID, "gpt-6-astra", svc.openAICodexTicketConfig())

	statuses, err := svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), u.calls.Load())
	require.Equal(t, "http://harvest.example:8080", u.proxy)
	require.Len(t, statuses, 1)
	require.Equal(t, "gpt-6-astra", statuses[0].Model)
	require.True(t, statuses[0].Ready)
	require.Equal(t, 292, statuses[0].Length)
	require.False(t, statuses[0].Blocked)
	require.NotNil(t, repo.updates[openAICodexTicketExtraKey("gpt-6-astra")])
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
}

func TestRefreshOpenAICodexTicketsForAccount_ModelFilter(t *testing.T) {
	account := ticketTestAccount(41)
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra", "gpt-5.6-sol"}, HarvestProxyURL: "http://harvest.example:8080"}
	svc, u, _ := codexRefreshService(t, cfg, *account)

	statuses, err := svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, []string{"gpt-5.6-sol", "not-gated"})
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	require.Equal(t, int64(1), u.calls.Load())
	require.Equal(t, "gpt-5.6-sol", svc.lookupOpenAICodexTicket(account, "gpt-5.6-sol").Model)
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))

	_, err = svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, []string{"not-gated"})
	require.ErrorIs(t, err, ErrOpenAICodexTicketModelNotGated)
}

func TestRefreshOpenAICodexTicketsForAccount_Errors(t *testing.T) {
	account := ticketTestAccount(41)

	svc, u, _ := codexRefreshService(t, config.OpenAICodexTicketConfig{Enabled: false, Models: []string{"gpt-6-astra"}}, *account)
	_, err := svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, nil)
	require.ErrorIs(t, err, ErrOpenAICodexTicketNotApplicable)
	require.Equal(t, int64(0), u.calls.Load())

	apiKey := ticketTestAccount(42)
	apiKey.Type = AccountTypeAPIKey
	svc, _, _ = codexRefreshService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, *apiKey)
	_, err = svc.RefreshOpenAICodexTicketsForAccount(context.Background(), apiKey.ID, nil)
	require.ErrorIs(t, err, ErrOpenAICodexTicketNotApplicable)

	svc = ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, nil)
	_, err = svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, nil)
	require.ErrorIs(t, err, ErrOpenAICodexTicketRefreshUnavailable)

	svc, _, _ = codexRefreshService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, *ticketTestAccount(43))
	svc.accountRepo = errAccountRepo{}
	_, err = svc.RefreshOpenAICodexTicketsForAccount(context.Background(), 43, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type errAccountRepo struct {
	AccountRepository
}

func (errAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, context.DeadlineExceeded
}

func TestRefreshOpenAICodexTicketsForAccount_OverwritesExpiredTicket(t *testing.T) {
	account := ticketTestAccount(41)
	svc, u, _ := codexRefreshService(t, config.OpenAICodexTicketConfig{
		Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://harvest.example:8080",
	}, *account)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{Cookie: "__cflb=c; __oailb=o", ProxyURL: "http://proxy.example:8080", ResponseModel: "gpt-6-astra",
		AccountID: account.ID, Model: "gpt-6-astra",
		State: fakeCodexTicketState(292), Length: 292,
		CapturedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour),
	})

	statuses, err := svc.RefreshOpenAICodexTicketsForAccount(context.Background(), account.ID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), u.calls.Load())
	require.True(t, statuses[0].Ready)
	fresh := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.True(t, fresh.ExpiresAt.After(time.Now()))
	require.Equal(t, "http://harvest.example:8080", fresh.ProxyURL)
}
