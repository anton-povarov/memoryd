package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/anton-povarov/memoryd/server/internal/webui"
	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

type Server struct {
	config     config.ServerConfig
	httpServer *http.Server
	logger     *slog.Logger
}

func New(
	version string,
	cfg config.Config,
	logger *slog.Logger,
	memoryVault *vault.Vault,
) (*Server, error) {
	if logger == nil {
		panic("server.New: must provide a logger")
	}

	logger = logging.NewChildLogger(logger, "http")

	httpHandler := NewHandler(version, logger, memoryVault)
	mux := http.NewServeMux()

	if err := RegisterDocumentationEndpoint(mux); err != nil {
		return nil, err
	}
	if err := webui.Register(mux); err != nil {
		return nil, err
	}

	strictHandler := api.NewStrictHandlerWithOptions(
		httpHandler,
		[]api.StrictMiddlewareFunc{
			func(next api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
				return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
					apiRequest := apiRequestFromContext(r.Context())
					apiRequest.operationID = operationID

					logger := logging.FromContext(r.Context())
					logger = logging.NewChildLogger(logger, operationID)
					r = r.WithContext(logging.ContextWithLogger(r.Context(), logger))

					logger.LogAttrs(r.Context(),
						slog.LevelInfo,
						debugColoredString(logger, ">> API request starting", ansiBlue),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.String("remote_address", r.RemoteAddr),
					)

					response := &responseRecorder{
						ResponseWriter: w,
						status:         http.StatusOK,
						committed:      false,
						contentLength:  0,
						errorMessage:   "",
					}

					result, err := next(r.Context(), response, r, request)

					// pass the error if any back to apiRequestMiddleware
					apiRequest.Err = err

					// returning err here is fine, as our error handler is empty
					// apiRequestMiddleware will do the job of logging it
					return result, err
				}
			},
		},
		api.StrictHTTPServerOptions{
			// default, will be called (TODO: figure out - maybe override it as well?)
			RequestErrorHandlerFunc: nil,
			// default, but will never be called, as middleware above hijacks error handling
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {},
		},
	)

	api.HandlerFromMuxWithBaseURL(
		strictHandler,
		mux,
		api.ServerUrlLocalMemorydServer,
	)

	handler :=
		requestIDMiddleware(
			apiRequestMiddleware(logger,
				panicRecoveryMiddleware(mux)))

	httpServer := new(http.Server)
	httpServer.Addr = cfg.Server.Address
	httpServer.Handler = handler
	httpServer.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)

	return &Server{
		config:     cfg.Server,
		httpServer: httpServer,
		logger:     logger,
	}, nil
}

// Handler exposes the complete HTTP surface for black-box tests and embedding.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Run serves until ctx is cancelled or the listener fails. Cancellation starts
// graceful shutdown and bounds request draining by the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	errorsFromServer := make(chan error, 1)
	go func() {
		s.logger.Info("HTTP server starting",
			"address", s.config.Address,
			"api_base", api.ServerUrlLocalMemorydServer,
			"docs", "/docs/",
		)
		errorsFromServer <- s.httpServer.ListenAndServe()
	}()

	select {
	case err := <-errorsFromServer:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.logger.Info(
		"HTTP server shutting down",
		"timeout",
		s.config.ShutdownTimeout,
	)
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		s.config.ShutdownTimeout,
	)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownContext); err != nil {
		return err
	}

	select {
	case err := <-errorsFromServer:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-time.After(s.config.ShutdownTimeout):
		return context.DeadlineExceeded
	}
	s.logger.Info("HTTP server stopped")
	return nil
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if requestID == "" {
			uu := uuid.New()
			requestID = base64.RawURLEncoding.EncodeToString(uu[:])
			r.Header.Set(requestIDHeader, requestID)
		}
		w.Header().Set(requestIDHeader, requestID)

		next.ServeHTTP(w, r)
	})
}

const (
	ansiReset  = "\x1b[0m"
	ansiBlue   = "\x1b[34m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

func debugColoredString(logger *slog.Logger, msg string, color string) string {
	if logging.IsDevelopment(logger) {
		return fmt.Sprintf("%s%s%s", color, msg, ansiReset)
	}
	return msg
}

func responseLogLevel(response *responseRecorder) slog.Level {
	if response.status >= http.StatusInternalServerError {
		return slog.LevelError
	} else if response.status >= http.StatusBadRequest {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func apiRequestMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	genericLogger := logging.NewSibling(logger, "")
	apiLogger := logging.NewSibling(logger, "api")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()

		response := &responseRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
			committed:      false,
			contentLength:  0,
			errorMessage:   "",
		}

		var rctx *apiRequestContext

		// might serve an API request, get prep'd
		if strings.Contains(r.URL.Path, api.ServerUrlLocalMemorydServer) {
			requestID := r.Header.Get(requestIDHeader)

			rctx = &apiRequestContext{
				startTime:   startedAt,
				request:     r,
				operationID: "",
			}

			r = r.WithContext(contextWithAPIRequest(r.Context(), rctx))

			// logger passed to request handler
			requestLogger := apiLogger.With("request_id", requestID)
			r = r.WithContext(logging.ContextWithLogger(r.Context(), requestLogger))

			next.ServeHTTP(response, r)

			// actually served an API request
			// and now we have to log whatever happened
			// this needs to be here and not in oapi-codegen middleware,
			// because response is written in generated code and we can't access it,
			// and here we can via `response`
			if rctx.operationID != "" {
				logAttrs := []slog.Attr{
					slog.String("path", r.URL.Path),
					slog.Int("status", response.status),
					slog.Int64("content_length", response.contentLength),
					slog.Duration("latency", time.Since(rctx.startTime)),
				}

				if rctx.Err != nil {
					http.Error(
						response,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
					logAttrs = append(logAttrs,
						slog.String("error", rctx.Err.Error()),
						slog.Int("status", http.StatusInternalServerError))
				}

				message := ""
				level := responseLogLevel(response)
				color := ansiGreen
				if level > slog.LevelInfo {
					color = ansiRed
				}
				message = debugColoredString(requestLogger, "<< API request completed", color)

				requestLogger.LogAttrs(r.Context(),
					level,
					message,
					logAttrs...,
				)

			} else { // happened to not be an API request, or a 404 or whatever the f

				level := responseLogLevel(response)

				attrs := []slog.Attr{
					slog.String("path", r.URL.Path),
					slog.Int("status", response.status),
					slog.Int64("content_length", response.contentLength),
					slog.Duration("latency", time.Since(startedAt)),
				}
				if errmsg := strings.TrimSpace(response.errorMessage); errmsg != "" {
					attrs = append(attrs, slog.String("error", errmsg))
				}

				requestLogger.LogAttrs(r.Context(),
					level,
					debugColoredString(logger, "Unexpected API request", ansiRed),
					attrs...)
			}

		} else { // non-api request for sure

			next.ServeHTTP(response, r)

			level := responseLogLevel(response)

			// very short logging if everything is ok in developer mode
			// don't want to flood the log with unimportant stuff
			if logging.IsDevelopment(genericLogger) && response.status < http.StatusBadRequest {
				genericLogger.DebugContext(r.Context(),
					fmt.Sprintf("HTTP static asset: %d %s", response.status, r.URL.Path))
			} else {
				// production logging or error response from the `next` handler
				attrs := []slog.Attr{
					slog.String("path", r.URL.Path),
					slog.Int("status", response.status),
					slog.Int64("content_length", response.contentLength),
					slog.Duration("latency", time.Since(startedAt)),
				}
				if errmsg := strings.TrimSpace(response.errorMessage); errmsg != "" {
					attrs = append(attrs, slog.String("error", errmsg))
				}

				logger.LogAttrs(r.Context(),
					level,
					"HTTP static asset",
					attrs...)
			}
		}
	})
}

func panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.FromContext(r.Context()).ErrorContext(
					r.Context(),
					"HTTP handler panicked",
					"method", r.Method,
					"path", r.URL.Path,
					"error", recovered,
					"stack", string(debug.Stack()),
				)
				if response, ok := w.(*responseRecorder); !ok || !response.committed {
					http.Error(
						w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
				}
			}
		}()

		next.ServeHTTP(w, r)
	})
}

type apiRequestContextKey struct{}

type apiRequestContext struct {
	startTime   time.Time
	request     *http.Request
	operationID string // out: from oapi-codegen middleware
	Err         error  // out: from oapi-codegen middleware
}

func contextWithAPIRequest(ctx context.Context, rctx *apiRequestContext) context.Context {
	return context.WithValue(ctx, apiRequestContextKey{}, rctx)
}

func apiRequestFromContext(ctx context.Context) *apiRequestContext {
	if rctx, ok := ctx.Value(apiRequestContextKey{}).(*apiRequestContext); ok {
		return rctx
	}
	return nil
}

type responseRecorder struct {
	http.ResponseWriter
	status        int
	committed     bool
	contentLength int64
	errorMessage  string
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.committed {
		return
	}

	r.status = status
	r.committed = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(content []byte) (int, error) {
	if !r.committed {
		r.WriteHeader(http.StatusOK)
	}

	r.contentLength += int64(len(content))

	if r.status >= http.StatusBadRequest {
		r.errorMessage += string(content)
	}

	return r.ResponseWriter.Write(content)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
