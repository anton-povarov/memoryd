package logging

import "context"

type requestIDContextKey struct{}

// ContextWithRequestID associates a request ID with work derived from ctx.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestID returns the request ID associated with ctx, or an empty string.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// ForOperation derives a logger with the reserved operation attribute.
func ForOperation(base *Logger, operation string) *Logger {
	return base.With(SysOperation(operation))
}

// ForOperationInRequest derives an operation logger and includes the request
// ID as an ordinary attribute when one is present in ctx.
func ForOperationInRequest(
	base *Logger,
	operation string,
	ctx context.Context,
) *Logger {
	logger := ForOperation(base, operation)
	if requestID := RequestID(ctx); requestID != "" {
		logger = logger.With("request_id", requestID)
	}

	return logger
}
