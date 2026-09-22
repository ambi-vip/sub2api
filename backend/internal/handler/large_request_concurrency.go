package handler

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	largeRequestSlotHeldContextKey = "sub2api.large_request_slot_held"
	largeRequestWaitMsContextKey   = "sub2api.large_request_wait_ms"
	largeRequestBodyBytesKey       = "sub2api.large_request_body_bytes"
)

// LargeRequestConcurrencyMiddleware acquires a slot before reading a request
// when Content-Length already proves that it is large. Unknown-length bodies
// are probed only up to the threshold before admission. Compressed bodies reserve
// a slot up front because their decoded size cannot be inferred from the headers.
func (h *OpenAIGatewayHandler) LargeRequestConcurrencyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.shouldLimitLargeResponsesRequest(c) {
			c.Next()
			return
		}

		threshold := h.largeRequestThresholdBytes()
		bodyBytes, source := c.Request.ContentLength, "content_length"
		encoding := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Encoding")))
		if encoding != "" && encoding != "identity" {
			source = "compressed_body"
		} else if bodyBytes < threshold {
			if bodyBytes >= 0 || c.Request.Body == nil {
				c.Next()
				return
			}
			// Probe before downstream middleware can buffer the entire body. Keep
			// the original closer and replay the prefix (and any read error).
			original := c.Request.Body
			prefix, err := io.ReadAll(io.LimitReader(original, threshold))
			var tail io.Reader = original
			if err != nil {
				tail = &largeRequestReadError{err: err}
			}
			c.Request.Body = &largeRequestReplayBody{Reader: io.MultiReader(bytes.NewReader(prefix), tail), Closer: original}
			if int64(len(prefix)) < threshold {
				c.Next()
				return
			}
			bodyBytes, source = int64(len(prefix)), "body_threshold"
		}
		release, acquired := h.acquireLargeRequestSlot(c, bodyBytes, source)
		if !acquired {
			c.Abort()
			return
		}
		if release != nil {
			defer release()
		}
		c.Next()
	}
}

type largeRequestReplayBody struct {
	io.Reader
	io.Closer
}

type largeRequestReadError struct{ err error }

func (r *largeRequestReadError) Read([]byte) (int, error) { return 0, r.err }

func (h *OpenAIGatewayHandler) shouldLimitLargeResponsesRequest(c *gin.Context) bool {
	if h == nil || h.cfg == nil || h.largeRequestLimiter == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	cfg := h.cfg.Gateway.LargeRequestConcurrency
	if !cfg.Enabled || cfg.ThresholdBytes <= 0 || cfg.MaxConcurrentRequests <= 0 || c.Request.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	switch {
	case path == "/responses", path == "/v1/responses", path == "/backend-api/codex/responses":
		return true
	case strings.HasPrefix(path, "/responses/"), strings.HasPrefix(path, "/v1/responses/"), strings.HasPrefix(path, "/backend-api/codex/responses/"):
		return true
	default:
		return false
	}
}

func (h *OpenAIGatewayHandler) largeRequestThresholdBytes() int64 {
	if h == nil || h.cfg == nil {
		return 0
	}
	return h.cfg.Gateway.LargeRequestConcurrency.ThresholdBytes
}

func (h *OpenAIGatewayHandler) acquireLargeRequestSlotAfterRead(c *gin.Context, bodyBytes int) (func(), bool) {
	if !h.shouldLimitLargeResponsesRequest(c) || int64(bodyBytes) < h.largeRequestThresholdBytes() {
		return nil, true
	}
	if held, _ := c.Get(largeRequestSlotHeldContextKey); held == true {
		c.Set(largeRequestBodyBytesKey, bodyBytes)
		return nil, true
	}
	return h.acquireLargeRequestSlot(c, int64(bodyBytes), "decoded_body")
}

func (h *OpenAIGatewayHandler) acquireLargeRequestSlot(c *gin.Context, bodyBytes int64, source string) (func(), bool) {
	cfg := h.cfg.Gateway.LargeRequestConcurrency
	wait := strings.EqualFold(strings.TrimSpace(cfg.OverflowMode), config.ImageConcurrencyOverflowModeWait)
	timeout := time.Duration(cfg.WaitTimeoutSeconds) * time.Second
	startedAt := time.Now()
	release, acquired := h.largeRequestLimiter.Acquire(
		c.Request.Context(), cfg.Enabled, cfg.MaxConcurrentRequests, wait, timeout, cfg.MaxWaitingRequests,
	)
	waitMs := time.Since(startedAt).Milliseconds()
	c.Set(largeRequestWaitMsContextKey, waitMs)
	c.Set(largeRequestBodyBytesKey, bodyBytes)

	log := logger.FromContext(c.Request.Context())
	if !acquired {
		retryAfter := cfg.WaitTimeoutSeconds
		if retryAfter <= 0 {
			retryAfter = 5
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		log.Warn("openai.large_request_concurrency_rejected",
			zap.Int64("body_bytes", bodyBytes),
			zap.Int64("threshold_bytes", cfg.ThresholdBytes),
			zap.Int("max_concurrent_requests", cfg.MaxConcurrentRequests),
			zap.String("overflow_mode", cfg.OverflowMode),
			zap.Int64("wait_ms", waitMs),
			zap.String("classification_source", source),
		)
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many large requests are being processed; retry later")
		return nil, false
	}

	c.Set(largeRequestSlotHeldContextKey, true)
	log.Info("openai.large_request_concurrency_acquired",
		zap.Int64("body_bytes", bodyBytes),
		zap.Int64("threshold_bytes", cfg.ThresholdBytes),
		zap.Int("max_concurrent_requests", cfg.MaxConcurrentRequests),
		zap.Int64("wait_ms", waitMs),
		zap.String("classification_source", source),
	)
	return release, true
}

func largeRequestTimingFromContext(c *gin.Context) (waitMs int64, bodyBytes int64, limited bool) {
	if c == nil {
		return 0, 0, false
	}
	if value, ok := c.Get(largeRequestWaitMsContextKey); ok {
		waitMs, _ = value.(int64)
	}
	if value, ok := c.Get(largeRequestBodyBytesKey); ok {
		switch v := value.(type) {
		case int64:
			bodyBytes = v
		case int:
			bodyBytes = int64(v)
		}
	}
	value, ok := c.Get(largeRequestSlotHeldContextKey)
	limited, _ = value.(bool)
	return waitMs, bodyBytes, ok && limited
}
