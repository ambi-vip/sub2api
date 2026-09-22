package handler

import (
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type openAIResponsesStageTiming struct {
	startedAt         time.Time
	bodyBytes         int
	handlerBodyReadMs int64
	securityAuditMs   int64
	upstreamAttempts  int
	model             string
	stream            bool
	streamCompleted   bool
	outcome           string
}

func newOpenAIResponsesStageTiming(startedAt time.Time) *openAIResponsesStageTiming {
	return &openAIResponsesStageTiming{startedAt: startedAt, outcome: "request_rejected"}
}

func int64Value(v int64) *int64       { return &v }
func float64Value(v float64) *float64 { return &v }
func boolValue(v bool) *bool          { return &v }

// buildOpenAIResponsesLatencyBreakdown freezes all request-owned timing data
// before usage recording moves to the async worker pool. Never read gin.Context
// from the worker: Gin may recycle it as soon as the handler returns.
func buildOpenAIResponsesLatencyBreakdown(c *gin.Context, timing *openAIResponsesStageTiming) *service.UsageLatencyBreakdown {
	if c == nil || timing == nil {
		return nil
	}
	now := time.Now()
	breakdown := &service.UsageLatencyBreakdown{
		HandlerTotalMs:    int64Value(now.Sub(timing.startedAt).Milliseconds()),
		HandlerBodyReadMs: int64Value(timing.handlerBodyReadMs),
		SecurityAuditMs:   int64Value(timing.securityAuditMs),
		UpstreamAttempts:  timing.upstreamAttempts,
		StreamCompleted:   boolValue(timing.streamCompleted),
		Outcome:           timing.outcome,
	}

	if snapshot, ok := middleware2.RequestBodyTimingFromRequest(c.Request); ok {
		breakdown.IngressBodyBytes = int64Value(snapshot.BytesRead)
		breakdown.IngressBodyComplete = boolValue(snapshot.Complete)
		if !snapshot.ObservedAt.IsZero() {
			breakdown.RequestTotalMs = int64Value(now.Sub(snapshot.ObservedAt).Milliseconds())
		}
		if !snapshot.FirstReadAt.IsZero() && !snapshot.ObservedAt.IsZero() {
			breakdown.IngressBodyWaitMs = int64Value(snapshot.FirstReadAt.Sub(snapshot.ObservedAt).Milliseconds())
		}
		if snapshot.Complete && !snapshot.FirstReadAt.IsZero() && !snapshot.FinishedAt.IsZero() {
			readDuration := snapshot.FinishedAt.Sub(snapshot.FirstReadAt)
			breakdown.IngressBodyReadMs = int64Value(readDuration.Milliseconds())
			if readDuration > 0 {
				breakdown.IngressBodyMiBPerSecond = float64Value(float64(snapshot.BytesRead) / (1024 * 1024) / readDuration.Seconds())
			}
		}
	}

	largeWaitMs, _, _ := largeRequestTimingFromContext(c)
	breakdown.LargeRequestWaitMs = int64Value(largeWaitMs)
	for _, latency := range []struct {
		target **int64
		key    string
	}{
		{target: &breakdown.PreflightMs, key: service.OpsAuthLatencyMsKey},
		{target: &breakdown.RoutingMs, key: service.OpsRoutingLatencyMsKey},
		{target: &breakdown.UpstreamResponseHeaderMs, key: service.OpsUpstreamLatencyMsKey},
		{target: &breakdown.UpstreamConnectionAcquireMs, key: service.OpsUpstreamConnectionAcquireMsKey},
		{target: &breakdown.UpstreamRequestWriteMs, key: service.OpsUpstreamRequestWriteMsKey},
		{target: &breakdown.UpstreamFirstByteWaitMs, key: service.OpsUpstreamFirstByteWaitMsKey},
		{target: &breakdown.ResponseStreamMs, key: service.OpsResponseLatencyMsKey},
		{target: &breakdown.TimeToFirstTokenMs, key: service.OpsTimeToFirstTokenMsKey},
	} {
		if value, ok := getContextInt64(c, latency.key); ok {
			*latency.target = int64Value(value)
		}
	}
	if reused, ok := c.Get(service.OpsUpstreamConnectionReusedKey); ok {
		if value, valid := reused.(bool); valid {
			breakdown.UpstreamConnectionReused = boolValue(value)
		}
	}
	return breakdown
}

func logOpenAIResponsesStageTiming(c *gin.Context, reqLog *zap.Logger, timing *openAIResponsesStageTiming, streamStarted bool, largeRequestThreshold int64) {
	if c == nil || reqLog == nil || timing == nil {
		return
	}
	fields := []zap.Field{
		zap.Int("status_code", c.Writer.Status()),
		zap.Int64("handler_total_ms", time.Since(timing.startedAt).Milliseconds()),
		zap.Int("body_bytes", timing.bodyBytes),
		zap.Int64("handler_body_read_ms", timing.handlerBodyReadMs),
		zap.Int64("security_audit_ms", timing.securityAuditMs),
		zap.Int("upstream_attempts", timing.upstreamAttempts),
		zap.String("model", timing.model),
		zap.Bool("stream", timing.stream),
		zap.Bool("stream_started", streamStarted),
		zap.Bool("stream_completed", timing.streamCompleted),
		zap.String("outcome", timing.outcome),
	}

	if snapshot, ok := middleware2.RequestBodyTimingFromRequest(c.Request); ok {
		fields = append(fields,
			zap.Int64("ingress_body_bytes", snapshot.BytesRead),
			zap.Bool("ingress_body_complete", snapshot.Complete),
		)
		if !snapshot.ObservedAt.IsZero() {
			fields = append(fields, zap.Int64("request_total_ms", time.Since(snapshot.ObservedAt).Milliseconds()))
		}
		if !snapshot.FirstReadAt.IsZero() && !snapshot.ObservedAt.IsZero() {
			fields = append(fields, zap.Int64("ingress_body_wait_ms", snapshot.FirstReadAt.Sub(snapshot.ObservedAt).Milliseconds()))
		}
		if snapshot.Complete && !snapshot.FirstReadAt.IsZero() && !snapshot.FinishedAt.IsZero() {
			readDuration := snapshot.FinishedAt.Sub(snapshot.FirstReadAt)
			readMs := readDuration.Milliseconds()
			fields = append(fields, zap.Int64("ingress_body_read_ms", readMs))
			if !snapshot.ObservedAt.IsZero() {
				fields = append(fields, zap.Int64("ingress_body_total_ms", snapshot.FinishedAt.Sub(snapshot.ObservedAt).Milliseconds()))
			}
			if readDuration > 0 {
				mibPerSecond := float64(snapshot.BytesRead) / (1024 * 1024) / readDuration.Seconds()
				fields = append(fields, zap.Float64("ingress_body_mib_per_second", mibPerSecond))
			}
		}
	}

	largeWaitMs, classifiedBodyBytes, limited := largeRequestTimingFromContext(c)
	isLargeRequest := limited || (largeRequestThreshold > 0 && (int64(timing.bodyBytes) >= largeRequestThreshold || classifiedBodyBytes >= largeRequestThreshold))
	fields = append(fields,
		zap.Bool("large_request", isLargeRequest),
		zap.Bool("large_request_slot_acquired", limited),
		zap.Int64("large_request_wait_ms", largeWaitMs),
	)

	for _, latency := range []struct {
		name string
		key  string
	}{
		{name: "preflight_ms", key: service.OpsAuthLatencyMsKey},
		{name: "routing_ms", key: service.OpsRoutingLatencyMsKey},
		{name: "upstream_response_header_ms", key: service.OpsUpstreamLatencyMsKey},
		{name: "upstream_connection_acquire_ms", key: service.OpsUpstreamConnectionAcquireMsKey},
		{name: "upstream_request_write_ms", key: service.OpsUpstreamRequestWriteMsKey},
		{name: "upstream_first_byte_wait_ms", key: service.OpsUpstreamFirstByteWaitMsKey},
		{name: "response_stream_ms", key: service.OpsResponseLatencyMsKey},
		{name: "time_to_first_token_ms", key: service.OpsTimeToFirstTokenMsKey},
	} {
		if value, ok := getContextInt64(c, latency.key); ok {
			fields = append(fields, zap.Int64(latency.name, value))
		}
	}
	if reused, ok := c.Get(service.OpsUpstreamConnectionReusedKey); ok {
		if value, valid := reused.(bool); valid {
			fields = append(fields, zap.Bool("upstream_connection_reused", value))
		}
	}
	reqLog.Info("openai.responses_stage_timing", fields...)
}
