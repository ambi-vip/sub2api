package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type harvestScopeUpstream struct {
	HTTPUpstream
	mu  sync.Mutex
	ids []int64
}

func (u *harvestScopeUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ids = append(u.ids, id)
	return nil, errors.New("synthetic probe failure")
}

func harvestScopeAccount(id int64, schedulable bool, groups ...int64) Account {
	a := ticketTestAccount(id)
	a.Status, a.Schedulable, a.GroupIDs = StatusActive, schedulable, groups
	return *a
}

func harvestScopeService(t *testing.T, raw string, accounts []Account, budget int) (*OpenAIGatewayService, *harvestScopeUpstream, *codexTicketSettingRepo) {
	t.Helper()
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	if raw != "" {
		repo.values[SettingKeyOpenAICodexTicketHarvestScope] = raw
	}
	u := &harvestScopeUpstream{}
	s := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://example.org:1234", MaxProbesPerRound: budget,
	}, u)
	s.settingService = NewSettingService(repo, &config.Config{})
	s.accountRepo = &codexTicketRefreshRepo{accounts: accounts}
	return s, u, repo
}

func TestCodexHarvestScopeFilteringAndDeduplication(t *testing.T) {
	a := harvestScopeAccount(1, true, 2, 24)
	b := harvestScopeAccount(2, true, 26)
	c := harvestScopeAccount(3, true)
	api := harvestScopeAccount(4, true, 2)
	api.Type = AccountTypeAPIKey
	other := harvestScopeAccount(5, true, 2)
	other.Platform = "grok"
	limited := harvestScopeAccount(6, true, 2)
	future := time.Now().Add(time.Hour)
	limited.RateLimitResetAt = &future
	inactive := harvestScopeAccount(7, true, 2)
	inactive.Status = "inactive"
	shadow := harvestScopeAccount(8, true, 2)
	parent := int64(1)
	shadow.ParentAccountID = &parent
	s, u, _ := harvestScopeService(t, `{"mode":"selected","group_ids":[2,24]}`, []Account{a, a, b, c, api, other, limited, inactive, shadow}, 20)
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1}, u.ids)
}

func TestCodexHarvestScopeEmptyInvalidAndStorageErrorFailClosed(t *testing.T) {
	for _, raw := range []string{`{"mode":"selected","group_ids":[]}`, `{`, `null`, `{"mode":"bad"}`, `{"mode":"all","group_ids":[-1]}`} {
		t.Run(raw, func(t *testing.T) {
			s, u, _ := harvestScopeService(t, raw, []Account{harvestScopeAccount(1, true, 2)}, 6)
			s.refreshOpenAICodexTickets(context.Background())
			require.Empty(t, u.ids)
		})
	}
	s, u, repo := harvestScopeService(t, "", []Account{harvestScopeAccount(1, true, 2)}, 6)
	repo.err = errors.New("database unavailable")
	s.refreshOpenAICodexTickets(context.Background())
	require.Empty(t, u.ids)
}

func TestCodexHarvestScopeLegacyAndRuntimeChange(t *testing.T) {
	s, u, repo := harvestScopeService(t, "", []Account{harvestScopeAccount(1, true, 2), harvestScopeAccount(2, true, 26)}, 1)
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1}, u.ids)
	repo.values[SettingKeyOpenAICodexTicketHarvestScope] = `{"mode":"selected","group_ids":[]}`
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1}, u.ids)
	repo.values[SettingKeyOpenAICodexTicketHarvestScope] = `{"mode":"selected","group_ids":[26]}`
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 2}, u.ids)
}

func TestCodexHarvestPriorityRoundRobinAndDeferredBudget(t *testing.T) {
	s, u, _ := harvestScopeService(t, "", []Account{
		harvestScopeAccount(9, false, 2), harvestScopeAccount(1, true, 2), harvestScopeAccount(2, true, 2),
	}, 1)
	s.refreshOpenAICodexTickets(context.Background())
	s.refreshOpenAICodexTickets(context.Background())
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 2, 9}, u.ids) // Deferred only after both front accounts cool down.
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(1, "gpt-6-astra"))
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(9, "gpt-6-astra"))
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 2, 9, 1}, u.ids)
}

func TestCodexHarvestPriorityCompletesFirstTierBeforeDeferred(t *testing.T) {
	s, u, _ := harvestScopeService(t, "", []Account{harvestScopeAccount(9, false, 2), harvestScopeAccount(1, true, 2)}, 6)
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 9}, u.ids)
}

func TestCodexHarvestPrioritySkipsFreshTicketsAndHonorsChangedScheduling(t *testing.T) {
	first := harvestScopeAccount(1, true, 2)
	second := harvestScopeAccount(2, false, 2)
	s, u, _ := harvestScopeService(t, "", []Account{first, second}, 1)
	s.storeOpenAICodexTicket(context.Background(), &first, &openAICodexTicket{
		Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Hour),
	})
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{2}, u.ids)
	// A formerly deferred account takes priority as soon as its switch changes.
	s, u, _ = harvestScopeService(t, "", []Account{first, second}, 1)
	first.Schedulable, second.Schedulable = false, true
	s.accountRepo = &codexTicketRefreshRepo{accounts: []Account{first, second}}
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{2}, u.ids)
}

func TestCodexHarvestScopeNormalization(t *testing.T) {
	scope, err := NormalizeCodexTicketHarvestScope(CodexTicketHarvestScope{Mode: "selected", GroupIDs: []int64{24, 2, 2}})
	require.NoError(t, err)
	require.Equal(t, []int64{2, 24}, scope.GroupIDs)
	_, err = NormalizeCodexTicketHarvestScope(CodexTicketHarvestScope{Mode: "selected", GroupIDs: []int64{0}})
	require.Error(t, err)
}

func TestCodexHarvestGroupPriorityAndSchedulableOrder(t *testing.T) {
	high := harvestScopeAccount(1, true, 2, 24)
	high.Priority = 100 // Membership priority, not global priority, wins.
	high.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}, {GroupID: 24, Priority: -100}}
	low := harvestScopeAccount(2, true, 2)
	low.Priority = -100
	low.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 20}}
	deferred := harvestScopeAccount(3, false, 2)
	deferred.AccountGroups = []AccountGroup{{GroupID: 2, Priority: -1000}}
	s, u, _ := harvestScopeService(t, `{"mode":"selected","group_ids":[2]}`, []Account{deferred, low, high}, 6)
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 2, 3}, u.ids)
	scope := CodexTicketHarvestScope{Mode: "selected", GroupIDs: []int64{2}}
	require.Equal(t, 10, scope.priority(&high)) // Ignore unselected group 24.
	scope.GroupIDs = []int64{2, 24}
	require.Equal(t, -100, scope.priority(&high))
}

func TestCodexHarvestGroupPriorityDoesNotRotateLowerPriorityAhead(t *testing.T) {
	high := harvestScopeAccount(1, true, 2)
	high.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 1}}
	low := harvestScopeAccount(2, true, 2)
	low.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}}
	s, u, _ := harvestScopeService(t, "", []Account{low, high}, 1)
	s.refreshOpenAICodexTickets(context.Background())
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(1, "gpt-6-astra"))
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 1}, u.ids)
	// Once the high-priority account is cooling down, the next bucket gets capacity.
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 1, 2}, u.ids)
}

func TestCodexHarvestEqualGroupPriorityRoundRobin(t *testing.T) {
	a, b := harvestScopeAccount(1, true, 2), harvestScopeAccount(2, true, 2)
	a.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}}
	b.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}}
	s, u, _ := harvestScopeService(t, "", []Account{a, b}, 1)
	s.refreshOpenAICodexTickets(context.Background())
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(1, "gpt-6-astra"))
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{1, 2}, u.ids)
}

func TestCodexHarvestAccountPriorityBreaksGroupPriorityTie(t *testing.T) {
	a, b := harvestScopeAccount(1, true, 2), harvestScopeAccount(2, true, 2)
	a.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}}
	b.AccountGroups = []AccountGroup{{GroupID: 2, Priority: 10}}
	a.Priority, b.Priority = 50, 5
	s, u, _ := harvestScopeService(t, "", []Account{a, b}, 6)
	s.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, []int64{2, 1}, u.ids)
}
