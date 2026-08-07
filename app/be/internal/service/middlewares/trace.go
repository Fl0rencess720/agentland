package middlewares

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func Trace() gin.HandlerFunc {
	tracer := otel.Tracer("agentland/app-be/http")
	return func(c *gin.Context) {
		ctx := otel.GetTextMapPropagator().Extract(
			c.Request.Context(),
			propagation.HeaderCarrier(c.Request.Header),
		)
		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		ctx, span := tracer.Start(ctx, fmt.Sprintf("%s %s", c.Request.Method, route), trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		requestID := strings.TrimSpace(c.GetHeader(observability.RequestIDHeader))
		if requestID == "" {
			requestID = observability.RequestIDFromContext(ctx)
		}
		ctx = observability.ContextWithRequestID(ctx, requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Writer.Header().Set(observability.RequestIDHeader, requestID)

		c.Next()

		if matched := c.FullPath(); matched != "" {
			route = matched
			span.SetName(fmt.Sprintf("%s %s", c.Request.Method, route))
		}
		status := c.Writer.Status()
		span.SetAttributes(
			attribute.String("request.id", requestID),
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		)
		if len(c.Errors) != 0 {
			err := fmt.Errorf("%s", c.Errors.String())
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}
