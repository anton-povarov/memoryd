package server

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
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

	openAPIYAML, err := api.OpenAPIYAML()
	if err != nil {
		return nil, err
	}
	if err := RegisterDocumentationEndpoint(mux, "", openAPIYAML); err != nil {
		return nil, err
	}
	if err := webui.Register(mux); err != nil {
		return nil, err
	}

	strictHandler := api.NewStrictHandlerWithOptions(
		httpHandler,
		nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: nil,
			ResponseErrorHandlerFunc: func(
				w http.ResponseWriter,
				r *http.Request,
				err error,
			) {
				logger := logging.FromContext(r.Context())
				logger.ErrorContext(
					r.Context(),
					"HTTP handler failed",
					"error", err,
				)
				http.Error(
					w,
					http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError,
				)
			},
		},
	)
	api.HandlerFromMuxWithBaseURL(
		strictHandler,
		mux,
		api.ServerUrlLocalMemorydServer,
	)

	handler := requestIDMiddleware(
		requestLoggerMiddleware(
			logger,
			panicRecoveryMiddleware(
				requestContextMiddleware(mux),
			),
		),
	)
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

func requestLoggerMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		requestID := r.Header.Get(requestIDHeader)

		requestLogger := logger.With("request_id", requestID)
		requestLogger = logging.NewChildLogger(requestLogger, requestID)
		r = r.WithContext(logging.WithLogger(r.Context(), requestLogger))
		response := &responseRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
			committed:      false,
		}

		requestLogger.LogAttrs(
			r.Context(),
			slog.LevelInfo,
			">> HTTP request starting",
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)

		next.ServeHTTP(response, r)

		level := slog.LevelInfo
		if response.status >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if response.status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		requestLogger.LogAttrs(
			r.Context(),
			level,
			"<< HTTP request completed",
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", response.status),
			slog.Duration("latency", time.Since(startedAt)),
			slog.String("remote_address", r.RemoteAddr),
		)
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

type requestContextKey struct{}

func requestContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), requestContextKey{}, r)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status    int
	committed bool
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

	return r.ResponseWriter.Write(content)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
