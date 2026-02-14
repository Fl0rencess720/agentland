package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func tracingMiddleware() gin.HandlerFunc {
	tracer := otel.Tracer("gateway.http")

	return func(c *gin.Context) {
		reqCtx := otel.GetTextMapPropagator().Extract(
			c.Request.Context(),
			headerCarrier(c.Request.Header),
		)

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}

		spanName := fmt.Sprintf("%s %s", c.Request.Method, route)
		reqCtx, span := tracer.Start(reqCtx, spanName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		requestID := strings.TrimSpace(c.GetHeader(observability.RequestIDHeader))
		if requestID == "" {
			requestID = observability.RequestIDFromContext(reqCtx)
		}
		reqCtx = observability.ContextWithRequestID(reqCtx, requestID)

		c.Request = c.Request.WithContext(reqCtx)
		c.Writer.Header().Set(observability.RequestIDHeader, requestID)

		span.SetAttributes(
			attribute.String("request.id", requestID),
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.route", route),
			attribute.String("http.target", c.Request.URL.Path),
		)

		c.Next()

		statusCode := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.status_code", statusCode))
		if len(c.Errors) > 0 {
			err := errors.New(c.Errors.String())
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return
		}
		if statusCode >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(statusCode))
		}
	}
}

type headerCarrier http.Header

func (h headerCarrier) Get(key string) string {
	return http.Header(h).Get(key)
}

func (h headerCarrier) Set(key string, value string) {
	http.Header(h).Set(key, value)
}

func (h headerCarrier) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}
