package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceExtractsIncomingContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	var received trace.SpanContext
	router := gin.New()
	router.Use(Trace())
	router.GET("/runs/:id", func(c *gin.Context) {
		received = trace.SpanContextFromContext(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/runs/run-1", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", received.TraceID().String())
	require.NotEmpty(t, recorder.Header().Get(observability.RequestIDHeader))
}
