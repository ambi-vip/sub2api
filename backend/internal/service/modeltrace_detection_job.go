package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/google/uuid"
)

const modelTraceJobRetention = 30 * time.Minute

type modelTraceDetectionJob struct {
	mu       sync.RWMutex
	jobID    string
	request  ModelTraceDetectionRequest
	method   string
	modelID  string
	started  time.Time
	finished *time.Time
	results  []ModelTraceAccountResult
}

// StartModelTraceDetection creates an observable background detection job. The
// HTTP caller receives all accounts immediately; workers then update each row
// independently as it moves from queued to running and finally to success/failure.
func (s *AccountTestService) StartModelTraceDetection(ctx context.Context, request ModelTraceDetectionRequest) (*ModelTraceDetectionResponse, error) {
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
	queued := 0
	for index, entry := range entries {
		if entry.account == nil {
			results[index] = ModelTraceAccountResult{
				AccountID: entry.missingID,
				Status:    "failed",
				Error:     "账号不存在或已删除",
			}
			continue
		}
		queued++
		results[index] = ModelTraceAccountResult{
			AccountID:   entry.account.ID,
			AccountName: entry.account.Name,
			Platform:    entry.account.Platform,
			AccountType: entry.account.Type,
			ModelID:     modelTraceTestModel(entry.account, modelID),
			Status:      "queued",
		}
	}
	job := &modelTraceDetectionJob{
		jobID:   uuid.NewString(),
		request: request,
		method:  bank.Method.Name,
		modelID: modelID,
		started: time.Now(),
		results: results,
	}
	s.storeModelTraceJob(job)
	if queued > 0 {
		go s.runModelTraceDetectionJob(job, entries)
	} else {
		job.markFinished()
	}
	return job.snapshot(), nil
}

// GetModelTraceDetection returns a point-in-time copy suitable for polling.
func (s *AccountTestService) GetModelTraceDetection(jobID string) (*ModelTraceDetectionResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("account test service unavailable")
	}
	s.modelTraceJobsMu.RLock()
	job := s.modelTraceJobs[jobID]
	s.modelTraceJobsMu.RUnlock()
	if job == nil {
		return nil, fmt.Errorf("ModelTrace detection job not found")
	}
	return job.snapshot(), nil
}

func (s *AccountTestService) storeModelTraceJob(job *modelTraceDetectionJob) {
	s.modelTraceJobsMu.Lock()
	defer s.modelTraceJobsMu.Unlock()
	if s.modelTraceJobs == nil {
		s.modelTraceJobs = make(map[string]*modelTraceDetectionJob)
	}
	cutoff := time.Now().Add(-modelTraceJobRetention)
	for id, existing := range s.modelTraceJobs {
		existing.mu.RLock()
		finished := existing.finished
		existing.mu.RUnlock()
		if finished != nil && finished.Before(cutoff) {
			delete(s.modelTraceJobs, id)
		}
	}
	s.modelTraceJobs[job.jobID] = job
}

func (s *AccountTestService) runModelTraceDetectionJob(job *modelTraceDetectionJob, entries []modelTraceAccountEntry) {
	workerCount := modelTraceConcurrency
	if len(entries) < workerCount {
		workerCount = len(entries)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				entry := entries[index]
				if entry.account == nil {
					continue
				}
				job.updateResult(index, ModelTraceAccountResult{
					AccountID:   entry.account.ID,
					AccountName: entry.account.Name,
					Platform:    entry.account.Platform,
					AccountType: entry.account.Type,
					ModelID:     modelTraceTestModel(entry.account, job.modelID),
					Status:      "running",
				})
				result := s.detectModelTraceAccountWithProgress(context.Background(), entry.account, job.modelID, func(progress ModelTraceAccountResult) {
					job.updateResult(index, progress)
				})
				job.updateResult(index, result)
			}
		}()
	}
	for index, entry := range entries {
		if entry.account != nil {
			jobs <- index
		}
	}
	close(jobs)
	wg.Wait()
	job.markFinished()
}

func (job *modelTraceDetectionJob) updateResult(index int, result ModelTraceAccountResult) {
	job.mu.Lock()
	if index >= 0 && index < len(job.results) {
		job.results[index] = cloneModelTraceAccountResult(result)
	}
	job.mu.Unlock()
}

func (job *modelTraceDetectionJob) markFinished() {
	job.mu.Lock()
	finished := time.Now()
	job.finished = &finished
	job.mu.Unlock()
}

func (job *modelTraceDetectionJob) snapshot() *ModelTraceDetectionResponse {
	job.mu.RLock()
	results := make([]ModelTraceAccountResult, len(job.results))
	for index, result := range job.results {
		results[index] = cloneModelTraceAccountResult(result)
	}
	started := job.started
	finished := job.finished
	jobID := job.jobID
	method := job.method
	modelID := job.modelID
	request := job.request
	job.mu.RUnlock()

	response := &ModelTraceDetectionResponse{
		JobID:       jobID,
		Scope:       request.Scope,
		Total:       len(results),
		Method:      method,
		ModelID:     modelID,
		StartedAt:   started,
		FinishedAt:  finished,
		Concurrency: modelTraceConcurrency,
		Results:     results,
	}
	if len(results) < response.Concurrency {
		response.Concurrency = len(results)
	}
	for _, result := range results {
		switch result.Status {
		case "queued":
			response.Queued++
		case "running":
			response.Running++
		case "success":
			response.Completed++
			response.Detected++
		case "failed":
			response.Completed++
			response.Failed++
		}
	}
	if response.Total > 0 {
		response.Progress = response.Completed * 100 / response.Total
	}
	if finished != nil {
		response.Status = "succeeded"
		response.DurationMS = finished.Sub(started).Milliseconds()
	} else if response.Running > 0 || response.Completed > 0 {
		response.Status = "running"
		response.DurationMS = time.Since(started).Milliseconds()
	} else {
		response.Status = "queued"
		response.DurationMS = time.Since(started).Milliseconds()
	}
	response.Predictions = summarizeModelTracePredictions(results)
	return response
}

func summarizeModelTracePredictions(results []ModelTraceAccountResult) []ModelTracePredictionSummary {
	type aggregate struct {
		name        string
		count       int
		probability float64
	}
	byPrediction := make(map[string]*aggregate)
	for _, result := range results {
		if result.Status != "success" {
			continue
		}
		item := byPrediction[result.Prediction]
		if item == nil {
			item = &aggregate{name: result.PredictionName}
			byPrediction[result.Prediction] = item
		}
		item.count++
		item.probability += result.Probability
	}
	summary := make([]ModelTracePredictionSummary, 0, len(byPrediction))
	for prediction, item := range byPrediction {
		summary = append(summary, ModelTracePredictionSummary{
			Prediction:         prediction,
			PredictionName:     item.name,
			Count:              item.count,
			AverageProbability: item.probability / float64(item.count),
		})
	}
	sort.Slice(summary, func(i, j int) bool {
		if summary[i].Count != summary[j].Count {
			return summary[i].Count > summary[j].Count
		}
		return summary[i].Prediction < summary[j].Prediction
	})
	return summary
}

func cloneModelTraceAccountResult(result ModelTraceAccountResult) ModelTraceAccountResult {
	result.Errors = append([]string(nil), result.Errors...)
	result.Candidates = append([]modeltrace.Candidate(nil), result.Candidates...)
	return result
}
