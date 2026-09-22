package middleware

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type requestBodyTimingContextKey struct{}

// RequestBodyTimingSnapshot describes the first network/body read performed for
// a request. The snapshot survives request-body prereads and replacements, so a
// downstream handler can still distinguish client upload time from later work.
type RequestBodyTimingSnapshot struct {
	ObservedAt  time.Time
	FirstReadAt time.Time
	FinishedAt  time.Time
	BytesRead   int64
	Complete    bool
}

type requestBodyTimingState struct {
	mu          sync.Mutex
	observedAt  time.Time
	firstReadAt time.Time
	finishedAt  time.Time
	bytesRead   int64
	complete    bool
}

type timedRequestBody struct {
	body  io.ReadCloser
	state *requestBodyTimingState
}

// RequestBodyTiming observes the original inbound request body. Install it
// before request-size and body-preread middleware so the recorded bytes and
// duration represent the client-facing ingress read rather than a later read
// from an in-memory replacement body.
func RequestBodyTiming() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Body == nil {
			c.Next()
			return
		}
		state := &requestBodyTimingState{observedAt: time.Now()}
		c.Request.Body = &timedRequestBody{body: c.Request.Body, state: state}
		ctx := context.WithValue(c.Request.Context(), requestBodyTimingContextKey{}, state)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (b *timedRequestBody) Read(p []byte) (int, error) {
	if b == nil || b.body == nil {
		return 0, io.EOF
	}
	now := time.Now()
	b.state.mu.Lock()
	if b.state.firstReadAt.IsZero() {
		b.state.firstReadAt = now
	}
	b.state.mu.Unlock()

	n, err := b.body.Read(p)
	finishedAt := time.Now()
	b.state.mu.Lock()
	b.state.bytesRead += int64(n)
	if err != nil {
		b.state.complete = err == io.EOF
		if b.state.complete && b.state.finishedAt.IsZero() {
			b.state.finishedAt = finishedAt
		}
	}
	b.state.mu.Unlock()
	return n, err
}

func (b *timedRequestBody) Close() error {
	if b == nil || b.body == nil {
		return nil
	}
	return b.body.Close()
}

// RequestBodyTimingFromRequest returns a concurrency-safe snapshot of ingress
// body-read progress. Complete is true only after the original body reached a
// terminal read, avoiding misleading throughput calculations for partial reads.
func RequestBodyTimingFromRequest(req *http.Request) (RequestBodyTimingSnapshot, bool) {
	if req == nil {
		return RequestBodyTimingSnapshot{}, false
	}
	state, ok := req.Context().Value(requestBodyTimingContextKey{}).(*requestBodyTimingState)
	if !ok || state == nil {
		return RequestBodyTimingSnapshot{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return RequestBodyTimingSnapshot{
		ObservedAt:  state.observedAt,
		FirstReadAt: state.firstReadAt,
		FinishedAt:  state.finishedAt,
		BytesRead:   state.bytesRead,
		Complete:    state.complete,
	}, true
}
