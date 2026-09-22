package service

import (
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type opsUpstreamHTTPTrace struct {
	mu        sync.Mutex
	startedAt time.Time
	gotConnAt time.Time
	wroteAt   time.Time
}

// withOpsUpstreamHTTPTrace adds low-overhead phase timing to one upstream HTTP
// attempt. It intentionally records connection acquisition as one phase: that
// includes proxy/DNS/TCP/TLS work on a new connection and is near-zero on a
// reused connection.
func withOpsUpstreamHTTPTrace(c *gin.Context, req *http.Request, startedAt time.Time) *http.Request {
	if c == nil || req == nil {
		return req
	}
	state := &opsUpstreamHTTPTrace{startedAt: startedAt}
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			now := time.Now()
			state.mu.Lock()
			state.gotConnAt = now
			state.mu.Unlock()
			SetOpsLatencyMs(c, OpsUpstreamConnectionAcquireMsKey, now.Sub(startedAt).Milliseconds())
			c.Set(OpsUpstreamConnectionReusedKey, info.Reused)
		},
		WroteRequest: func(_ httptrace.WroteRequestInfo) {
			now := time.Now()
			state.mu.Lock()
			phaseStart := state.gotConnAt
			if phaseStart.IsZero() {
				phaseStart = state.startedAt
			}
			state.wroteAt = now
			state.mu.Unlock()
			SetOpsLatencyMs(c, OpsUpstreamRequestWriteMsKey, now.Sub(phaseStart).Milliseconds())
		},
		GotFirstResponseByte: func() {
			now := time.Now()
			state.mu.Lock()
			phaseStart := state.wroteAt
			if phaseStart.IsZero() {
				phaseStart = state.startedAt
			}
			state.mu.Unlock()
			SetOpsLatencyMs(c, OpsUpstreamFirstByteWaitMsKey, now.Sub(phaseStart).Milliseconds())
		},
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}
