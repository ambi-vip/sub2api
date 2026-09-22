package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyTimingRecordsOriginalIngressRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestBodyTiming())
	router.POST("/responses", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		snapshot, ok := RequestBodyTimingFromRequest(c.Request)
		require.True(t, ok)
		require.True(t, snapshot.Complete)
		require.Equal(t, int64(len(body)), snapshot.BytesRead)
		require.False(t, snapshot.ObservedAt.IsZero())
		require.False(t, snapshot.FirstReadAt.IsZero())
		require.False(t, snapshot.FinishedAt.IsZero())
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-test"}`))
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}
