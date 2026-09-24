package service

import "testing"

func TestModelTraceTestModelPrefersExplicitModel(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI}

	if got := modelTraceTestModel(account, "  custom-model  "); got != "custom-model" {
		t.Fatalf("expected trimmed explicit model, got %q", got)
	}
}

func TestModelTraceTestModelKeepsPlatformDefaults(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     string
	}{
		{name: "openai", platform: PlatformOpenAI, want: "gpt-5.4"},
		{name: "anthropic", platform: PlatformAnthropic, want: "claude-sonnet-4-6"},
		{name: "gemini", platform: PlatformGemini, want: "gemini-2.0-flash"},
		{name: "grok", platform: PlatformGrok, want: "grok-4.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelTraceTestModel(&Account{Platform: tt.platform}, ""); got != tt.want {
				t.Fatalf("expected platform default %q, got %q", tt.want, got)
			}
		})
	}
}

func TestEligibleModelTraceAccountEntriesOnlyIncludeActiveSchedulableAccounts(t *testing.T) {
	accounts := []Account{
		{ID: 1, Status: StatusActive, Schedulable: true},
		{ID: 2, Status: StatusActive, Schedulable: false},
		{ID: 3, Status: StatusError, Schedulable: true},
		{ID: 4, Status: StatusDisabled, Schedulable: true},
	}

	entries := eligibleModelTraceAccountEntries(accounts)
	if len(entries) != 1 {
		t.Fatalf("expected one eligible account, got %d", len(entries))
	}
	if entries[0].account == nil || entries[0].account.ID != 1 {
		t.Fatalf("expected account 1 to be eligible, got %#v", entries[0].account)
	}
}

func TestBuildModelTraceSelectedEntriesKeepsEverySelectedAccount(t *testing.T) {
	accountByID := map[int64]*Account{
		1: {ID: 1, Name: "ok", Status: StatusActive, Schedulable: true},
		2: {ID: 2, Name: "paused", Status: StatusActive, Schedulable: false},
		3: {ID: 3, Name: "errored", Status: StatusError, Schedulable: true},
		4: {ID: 4, Name: "disabled", Status: StatusDisabled, Schedulable: true},
	}
	ids := []int64{1, 2, 3, 4, 99}

	entries := buildModelTraceSelectedEntries(ids, accountByID)
	if len(entries) != len(ids) {
		t.Fatalf("expected %d entries for %d selected IDs, got %d", len(ids), len(ids), len(entries))
	}

	expectations := []struct {
		id         int64
		missing    bool
		skipReason string
	}{
		{id: 1},
		{id: 2, skipReason: "账号已暂停调度，已跳过检测"},
		{id: 3, skipReason: "账号处于错误状态，已跳过检测"},
		{id: 4, skipReason: "账号已被禁用，已跳过检测"},
		{id: 99, missing: true},
	}
	for index, want := range expectations {
		entry := entries[index]
		if want.missing {
			if entry.account != nil || entry.missingID != want.id {
				t.Fatalf("entry %d: expected missing id %d, got account=%#v missingID=%d", index, want.id, entry.account, entry.missingID)
			}
			continue
		}
		if entry.account == nil || entry.account.ID != want.id {
			t.Fatalf("entry %d: expected account %d, got account=%#v missingID=%d", index, want.id, entry.account, entry.missingID)
		}
		if entry.skipReason != want.skipReason {
			t.Fatalf("entry %d (account %d): expected skip reason %q, got %q", index, want.id, want.skipReason, entry.skipReason)
		}
	}
}

func TestModelTraceSkippedResultCarriesAccountInfoAndReason(t *testing.T) {
	account := &Account{ID: 7, Name: "paused", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: false}

	result := modelTraceSkippedResult(account, "", "账号已暂停调度，已跳过检测")
	if result.AccountID != account.ID || result.AccountName != account.Name || result.Platform != account.Platform {
		t.Fatalf("expected account info to be preserved, got %#v", result)
	}
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("expected failed status with reason, got status=%q error=%q", result.Status, result.Error)
	}
	if result.ModelID != modelTraceTestModel(account, "") {
		t.Fatalf("expected platform default model %q, got %q", modelTraceTestModel(account, ""), result.ModelID)
	}
}
