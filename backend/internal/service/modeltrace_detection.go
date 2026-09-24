package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"golang.org/x/sync/errgroup"
)

const (
	modelTraceTargetOutputs = 3
	modelTraceMaxAttempts   = 6
	modelTraceConcurrency   = 6
)

type ModelTraceDetectionRequest struct {
	Scope      string  `json:"scope"`
	AccountIDs []int64 `json:"account_ids"`
	GroupID    int64   `json:"group_id"`
	ModelID    string  `json:"model_id,omitempty"`
}

type ModelTracePredictionSummary struct {
	Prediction         string  `json:"prediction"`
	PredictionName     string  `json:"prediction_name"`
	Count              int     `json:"count"`
	AverageProbability float64 `json:"average_probability"`
}

type ModelTraceAccountResult struct {
	AccountID         int64                  `json:"account_id"`
	AccountName       string                 `json:"account_name"`
	Platform          string                 `json:"platform"`
	AccountType       string                 `json:"account_type"`
	ModelID           string                 `json:"model_id,omitempty"`
	Status            string                 `json:"status"`
	Error             string                 `json:"error,omitempty"`
	Errors            []string               `json:"errors,omitempty"`
	Attempts          int                    `json:"attempts"`
	UsedOutputs       int                    `json:"used_outputs"`
	LatencyMS         int64                  `json:"latency_ms"`
	Prediction        string                 `json:"prediction,omitempty"`
	PredictionName    string                 `json:"prediction_name,omitempty"`
	Probability       float64                `json:"probability,omitempty"`
	FamilyPrediction  string                 `json:"family_prediction,omitempty"`
	FamilyName        string                 `json:"family_name,omitempty"`
	FamilyProbability float64                `json:"family_probability,omitempty"`
	Candidates        []modeltrace.Candidate `json:"candidates,omitempty"`
}

type ModelTraceDetectionResponse struct {
	JobID       string                        `json:"job_id,omitempty"`
	Status      string                        `json:"status,omitempty"`
	Scope       string                        `json:"scope"`
	Total       int                           `json:"total"`
	Detected    int                           `json:"detected"`
	Failed      int                           `json:"failed"`
	Queued      int                           `json:"queued"`
	Running     int                           `json:"running"`
	Completed   int                           `json:"completed"`
	Progress    int                           `json:"progress_percent"`
	Concurrency int                           `json:"concurrency"`
	ModelID     string                        `json:"model_id,omitempty"`
	StartedAt   time.Time                     `json:"started_at,omitempty"`
	FinishedAt  *time.Time                    `json:"finished_at,omitempty"`
	DurationMS  int64                         `json:"duration_ms"`
	Method      string                        `json:"method"`
	Predictions []ModelTracePredictionSummary `json:"predictions"`
	Results     []ModelTraceAccountResult     `json:"results"`
}

type modelTraceAccountEntry struct {
	account   *Account
	missingID int64
}

// DetectModelTrace resolves the requested account scope and probes each account
// with bounded concurrency. Each per-account result remains independent, so one
// upstream failure does not prevent the rest of the batch from completing.
func (s *AccountTestService) DetectModelTrace(ctx context.Context, request ModelTraceDetectionRequest) (*ModelTraceDetectionResponse, error) {
	if s == nil || s.accountRepo == nil {
		return nil, fmt.Errorf("account repository is unavailable")
	}
	bank, err := modeltrace.LoadBank()
	if err != nil {
		return nil, fmt.Errorf("load ModelTrace reference bank: %w", err)
	}
	entries, err := s.resolveModelTraceAccounts(ctx, request)
	if err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(request.ModelID)
	if modelID == "" {
		modelID = s.modelTraceDefaultModel(ctx)
	}

	results := make([]ModelTraceAccountResult, len(entries))
	if len(entries) == 0 {
		return &ModelTraceDetectionResponse{
			Scope:   request.Scope,
			Total:   0,
			Method:  bank.Method.Name,
			Results: results,
		}, nil
	}
	group, groupCtx := errgroup.WithContext(ctx)
	jobs := make(chan int)
	workerCount := modelTraceConcurrency
	if len(entries) < workerCount {
		workerCount = len(entries)
	}
	for worker := 0; worker < workerCount; worker++ {
		group.Go(func() error {
			for index := range jobs {
				item := entries[index]
				if item.account == nil {
					results[index] = ModelTraceAccountResult{AccountID: item.missingID, Status: "failed", Error: "账号不存在或已删除"}
					continue
				}
				results[index] = s.detectModelTraceAccount(groupCtx, item.account, modelID)
			}
			return nil
		})
	}
	for index := range entries {
		jobs <- index
	}
	close(jobs)
	if err := group.Wait(); err != nil {
		return nil, err
	}

	response := &ModelTraceDetectionResponse{
		Scope:   request.Scope,
		Total:   len(results),
		Method:  bank.Method.Name,
		Results: results,
	}
	type predictionAggregate struct {
		name        string
		count       int
		probability float64
	}
	byPrediction := make(map[string]*predictionAggregate)
	for _, result := range results {
		if result.Status == "success" {
			response.Detected++
			aggregate := byPrediction[result.Prediction]
			if aggregate == nil {
				aggregate = &predictionAggregate{name: result.PredictionName}
				byPrediction[result.Prediction] = aggregate
			}
			aggregate.count++
			aggregate.probability += result.Probability
		} else {
			response.Failed++
		}
	}
	for prediction, aggregate := range byPrediction {
		response.Predictions = append(response.Predictions, ModelTracePredictionSummary{
			Prediction:         prediction,
			PredictionName:     aggregate.name,
			Count:              aggregate.count,
			AverageProbability: aggregate.probability / float64(aggregate.count),
		})
	}
	sort.Slice(response.Predictions, func(i, j int) bool {
		if response.Predictions[i].Count != response.Predictions[j].Count {
			return response.Predictions[i].Count > response.Predictions[j].Count
		}
		return response.Predictions[i].Prediction < response.Predictions[j].Prediction
	})
	return response, nil
}

func (s *AccountTestService) resolveModelTraceAccounts(ctx context.Context, request ModelTraceDetectionRequest) ([]modelTraceAccountEntry, error) {
	switch request.Scope {
	case "all":
		accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", StatusActive, "", 0, "")
		if err != nil {
			return nil, fmt.Errorf("list accounts for ModelTrace: %w", err)
		}
		return eligibleModelTraceAccountEntries(accounts), nil
	case "group":
		if request.GroupID <= 0 {
			return nil, fmt.Errorf("group_id must be a positive ID")
		}
		accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", StatusActive, "", request.GroupID, "")
		if err != nil {
			return nil, fmt.Errorf("list group accounts for ModelTrace: %w", err)
		}
		return eligibleModelTraceAccountEntries(accounts), nil
	case "selected":
		ids := make([]int64, 0, len(request.AccountIDs))
		seen := make(map[int64]struct{}, len(request.AccountIDs))
		for _, id := range request.AccountIDs {
			if id <= 0 {
				return nil, fmt.Errorf("account_ids must contain positive IDs")
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("account_ids must contain at least one ID")
		}
		accounts, err := s.accountRepo.GetByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load selected accounts for ModelTrace: %w", err)
		}
		accountByID := make(map[int64]*Account, len(accounts))
		for _, account := range accounts {
			if account != nil {
				accountByID[account.ID] = account
			}
		}
		entries := make([]modelTraceAccountEntry, 0, len(accounts))
		for _, id := range ids {
			account := accountByID[id]
			if isEligibleModelTraceAccount(account) {
				entries = append(entries, modelTraceAccountEntry{account: account})
			}
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("scope must be all, selected, or group")
	}
}

// ModelTrace only probes accounts that are in a normal, enabled state. The
// status and schedulable flags are persistent admin controls; transient quota
// or rate-limit state is intentionally left to the normal account test path.
func isEligibleModelTraceAccount(account *Account) bool {
	return account != nil && account.Status == StatusActive && account.Schedulable
}

func eligibleModelTraceAccountEntries(accounts []Account) []modelTraceAccountEntry {
	entries := make([]modelTraceAccountEntry, 0, len(accounts))
	for index := range accounts {
		if isEligibleModelTraceAccount(&accounts[index]) {
			entries = append(entries, modelTraceAccountEntry{account: &accounts[index]})
		}
	}
	return entries
}

type modelTraceAccountProgress func(ModelTraceAccountResult)

func (s *AccountTestService) detectModelTraceAccount(ctx context.Context, account *Account, modelID string) ModelTraceAccountResult {
	return s.detectModelTraceAccountWithProgress(ctx, account, modelID, nil)
}

func (s *AccountTestService) detectModelTraceAccountWithProgress(ctx context.Context, account *Account, modelID string, progress modelTraceAccountProgress) ModelTraceAccountResult {
	startedAt := time.Now()
	result := ModelTraceAccountResult{
		AccountID:   account.ID,
		AccountName: account.Name,
		Platform:    account.Platform,
		AccountType: account.Type,
		ModelID:     modelTraceTestModel(account, modelID),
		Status:      "failed",
	}
	result.Status = "running"
	if progress != nil {
		progress(result)
	}
	challenges := modeltrace.GenerateChallenges(modelTraceMaxAttempts)
	outputs := make([]modeltrace.Output, 0, modelTraceTargetOutputs)
	for _, challenge := range challenges {
		if len(outputs) >= modelTraceTargetOutputs {
			break
		}
		if err := ctx.Err(); err != nil {
			result.Errors = append(result.Errors, compactModelTraceError(err.Error()))
			break
		}
		result.Attempts++
		if progress != nil {
			progress(result)
		}
		testResult, err := s.RunTestPromptBackground(ctx, account.ID, result.ModelID, challenge.Prompt)
		if err != nil {
			result.Errors = append(result.Errors, compactModelTraceError(err.Error()))
			if progress != nil {
				progress(result)
			}
			continue
		}
		if testResult.Status != "success" {
			message := strings.TrimSpace(testResult.ErrorMessage)
			if message == "" {
				message = "账号测试未完成"
			}
			result.Errors = append(result.Errors, compactModelTraceError(message))
			if progress != nil {
				progress(result)
			}
			continue
		}
		numberCount := len(modeltrace.ParseNumbers(testResult.ResponseText))
		minimum := maxInt(80, int(math.Ceil(float64(challenge.ExpectedCount)*0.55)))
		if numberCount < minimum {
			result.Errors = append(result.Errors, fmt.Sprintf("有效数字不足：%d/%d", numberCount, minimum))
			if progress != nil {
				progress(result)
			}
			continue
		}
		outputs = append(outputs, modeltrace.Output{Text: testResult.ResponseText, ExpectedCount: challenge.ExpectedCount})
		result.UsedOutputs = len(outputs)
		if progress != nil {
			progress(result)
		}
	}
	result.UsedOutputs = len(outputs)
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if len(outputs) == 0 {
		result.Error = "未获得可用于归因的有效回答"
		if len(result.Errors) > 0 {
			result.Error = result.Errors[len(result.Errors)-1]
		}
		if progress != nil {
			progress(result)
		}
		return result
	}
	analysis, err := modeltrace.Analyze(outputs)
	if err != nil {
		result.Error = compactModelTraceError(err.Error())
		if progress != nil {
			progress(result)
		}
		return result
	}
	result.Status = "success"
	result.Prediction = analysis.Prediction
	result.PredictionName = analysis.PredictionName
	result.Probability = analysis.Probability
	result.FamilyPrediction = analysis.FamilyPrediction
	result.FamilyName = analysis.FamilyPredictionName
	result.FamilyProbability = analysis.FamilyProbability
	result.Candidates = analysis.Results
	if len(result.Candidates) > 3 {
		result.Candidates = result.Candidates[:3]
	}
	if progress != nil {
		progress(result)
	}
	return result
}

func (s *AccountTestService) modelTraceDefaultModel(ctx context.Context) string {
	if s == nil || s.settingService == nil {
		return ""
	}
	settings, err := s.settingService.GetAllSettings(ctx)
	if err != nil || settings == nil {
		return ""
	}
	return strings.TrimSpace(settings.ModelTraceDefaultModel)
}

func modelTraceTestModel(account *Account, preferred string) string {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		return preferred
	}
	switch account.Platform {
	case PlatformOpenAI:
		return openai.DefaultTestModel
	case PlatformAnthropic:
		return "claude-sonnet-4-6"
	case PlatformAntigravity:
		return defaultAntigravityTestModel
	case PlatformGemini:
		return geminicli.DefaultTestModel
	case PlatformGrok:
		return grokDefaultResponsesModel
	case PlatformOpenCodeGo:
		return DefaultOpenCodeGoTestModel
	default:
		if account.IsCNProvider() {
			if account.GetAPIProtocol() == APIProtocolAnthropic {
				return "claude-sonnet-4-6"
			}
			return openai.DefaultTestModel
		}
		return ""
	}
}

func compactModelTraceError(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > 500 {
		return string(runes[:500]) + "…"
	}
	return string(runes)
}
