package handler

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompressedLargeRequestReservesBeforeDecode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	payload := strings.Repeat("x", 4096)
	_, err := writer.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Less(t, compressed.Len(), 1024)
	h := &OpenAIGatewayHandler{cfg: &config.Config{Gateway: config.GatewayConfig{
		LargeRequestConcurrency: config.LargeRequestConcurrencyConfig{Enabled: true, ThresholdBytes: 1024, MaxConcurrentRequests: 1},
	}}, largeRequestLimiter: &imageConcurrencyLimiter{}}
	router := gin.New()
	router.Use(h.LargeRequestConcurrencyMiddleware())
	router.POST("/responses", func(c *gin.Context) {
		require.Equal(t, 1, h.largeRequestLimiter.active)
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		require.NoError(t, err)
		require.Equal(t, payload, string(body))
		release, acquired := h.acquireLargeRequestSlotAfterRead(c, len(body))
		require.True(t, acquired)
		require.Nil(t, release, "must not acquire a second slot")
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Zero(t, h.largeRequestLimiter.active)
}

type countedLargeBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *countedLargeBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *countedLargeBody) Close() error { b.closed = true; return nil }

func TestLargeRequestAdmissionBoundsReadsAndReleases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, encoding string
		length         int64
		wantRead       int
	}{
		{"unknown", "", -1, 4},
		{"known", "", 100, 0},
		{"compressed", "gzip", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &OpenAIGatewayHandler{
				cfg: &config.Config{Gateway: config.GatewayConfig{LargeRequestConcurrency: config.LargeRequestConcurrencyConfig{
					Enabled: true, ThresholdBytes: 4, MaxConcurrentRequests: 1, OverflowMode: "reject",
				}}}, largeRequestLimiter: &imageConcurrencyLimiter{},
			}
			release, ok := h.largeRequestLimiter.TryAcquire(true, 1)
			require.True(t, ok)
			defer release()
			entered := false
			router := gin.New()
			router.Use(h.LargeRequestConcurrencyMiddleware())
			router.POST("/responses", func(c *gin.Context) {
				entered = true
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, strings.Repeat("x", 100), string(body))
				require.NoError(t, c.Request.Body.Close())
				c.Status(http.StatusNoContent)
			})
			makeRequest := func() (*http.Request, *countedLargeBody) {
				body := &countedLargeBody{Reader: strings.NewReader(strings.Repeat("x", 100))}
				req := httptest.NewRequest(http.MethodPost, "/responses", body)
				req.ContentLength = tc.length
				req.Header.Set("Content-Encoding", tc.encoding)
				return req, body
			}
			req, body := makeRequest()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.False(t, entered)
			require.Equal(t, tc.wantRead, body.read)
			release()
			for i := 0; i < 2; i++ {
				req, body = makeRequest()
				w = httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusNoContent, w.Code)
				require.True(t, body.closed)
				require.Zero(t, h.largeRequestLimiter.active)
			}
		})
	}
}

func TestLargeRequestProbePreservesSmallBodiesAndReadErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wantErr := errors.New("upload interrupted")
	for _, fail := range []bool{false, true} {
		h := &OpenAIGatewayHandler{cfg: &config.Config{Gateway: config.GatewayConfig{
			LargeRequestConcurrency: config.LargeRequestConcurrencyConfig{Enabled: true, ThresholdBytes: 4, MaxConcurrentRequests: 1},
		}}, largeRequestLimiter: &imageConcurrencyLimiter{}}
		release, _ := h.largeRequestLimiter.TryAcquire(true, 1)
		router := gin.New()
		router.Use(h.LargeRequestConcurrencyMiddleware())
		router.POST("/responses", func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			require.Equal(t, "abc", string(body))
			if fail {
				require.ErrorIs(t, err, wantErr)
			} else {
				require.NoError(t, err)
			}
			c.Status(http.StatusNoContent)
		})
		var reader io.Reader = strings.NewReader("abc")
		if fail {
			reader = io.MultiReader(reader, &largeRequestReadError{err: wantErr})
		}
		req := httptest.NewRequest(http.MethodPost, "/responses", io.NopCloser(reader))
		req.ContentLength = -1
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		release()
	}
}

func TestLargeRequestConcurrencyMiddlewareRejectsOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{
		cfg: &config.Config{Gateway: config.GatewayConfig{LargeRequestConcurrency: config.LargeRequestConcurrencyConfig{
			Enabled:               true,
			ThresholdBytes:        4,
			MaxConcurrentRequests: 1,
			OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		}}},
		largeRequestLimiter: &imageConcurrencyLimiter{},
	}

	entered := make(chan struct{})
	releaseFirst := make(chan struct{})
	router := gin.New()
	router.Use(h.LargeRequestConcurrencyMiddleware())
	router.POST("/responses", func(c *gin.Context) {
		close(entered)
		<-releaseFirst
		c.Status(http.StatusNoContent)
	})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader("12345"))
		router.ServeHTTP(recorder, request)
		firstDone <- recorder
	}()
	<-entered

	secondRecorder := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader("67890"))
	router.ServeHTTP(secondRecorder, secondRequest)
	require.Equal(t, http.StatusTooManyRequests, secondRecorder.Code)
	require.Equal(t, "5", secondRecorder.Header().Get("Retry-After"))

	close(releaseFirst)
	require.Equal(t, http.StatusNoContent, (<-firstDone).Code)
}
