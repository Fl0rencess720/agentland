package observability

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	TraceparentAnnotationKey = "observability.agentland.io/traceparent"
	TracestateAnnotationKey  = "observability.agentland.io/tracestate"
	RequestIDAnnotationKey   = "observability.agentland.io/request-id"
	RequestIDHeader          = "x-agentland-request-id"
)

type requestIDContextKey struct{}

// ContextWithRequestID stores request ID in context.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// RequestIDFromContext returns request ID from context, or derives one.
func RequestIDFromContext(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && requestID != "" {
		return requestID
	}

	spanCtx := trace.SpanFromContext(ctx).SpanContext()
	if spanCtx.IsValid() {
		return spanCtx.TraceID().String()
	}

	return uuid.NewString()
}

// InjectContextToAnnotations injects trace context and request ID into annotations.
func InjectContextToAnnotations(ctx context.Context, annotations map[string]string) map[string]string {
	if annotations == nil {
		annotations = map[string]string{}
	}

	requestID := RequestIDFromContext(ctx)
	if requestID != "" {
		annotations[RequestIDAnnotationKey] = requestID
	}

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if traceparent := carrier.Get("traceparent"); traceparent != "" {
		annotations[TraceparentAnnotationKey] = traceparent
	}
	if tracestate := carrier.Get("tracestate"); tracestate != "" {
		annotations[TracestateAnnotationKey] = tracestate
	}
	return annotations
}

// ExtractContextFromAnnotations restores trace context from resource annotations.
func ExtractContextFromAnnotations(ctx context.Context, annotations map[string]string) context.Context {
	if len(annotations) == 0 {
		return ctx
	}

	carrier := propagation.MapCarrier{}
	if traceparent, ok := annotations[TraceparentAnnotationKey]; ok && traceparent != "" {
		carrier.Set("traceparent", traceparent)
	}
	if tracestate, ok := annotations[TracestateAnnotationKey]; ok && tracestate != "" {
		carrier.Set("tracestate", tracestate)
	}
	if len(carrier) > 0 {
		ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	}

	if requestID, ok := annotations[RequestIDAnnotationKey]; ok && requestID != "" {
		ctx = ContextWithRequestID(ctx, requestID)
	}

	return ctx
}

// PropagateTraceAnnotations copies known tracing annotations from src into dst.
func PropagateTraceAnnotations(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	if len(src) == 0 {
		return dst
	}

	for _, key := range []string{
		TraceparentAnnotationKey,
		TracestateAnnotationKey,
		RequestIDAnnotationKey,
	} {
		if value, ok := src[key]; ok && value != "" {
			dst[key] = value
		}
	}
	return dst
}
