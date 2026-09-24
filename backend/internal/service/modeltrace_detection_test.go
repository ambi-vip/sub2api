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
