// Package server owns memoryd's HTTP runtime and process lifecycle.
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/anton-povarov/memoryd/server/api"
	"github.com/anton-povarov/memoryd/server/internal/apidoc"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Server struct {
	config config.ServerConfig
	echo   *echo.Echo
	logger *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger, handler api.StrictServerInterface) (*Server, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Logger.SetOutput(io.Discard)
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(requestLogger(logger))

	if err := apidoc.RegisterDocumentationEndpoint(e, "", api.OpenapiYAML); err != nil {
		return nil, err
	}
	api.RegisterHandlersWithBaseURL(e, api.NewStrictHandler(handler, nil), api.ServerUrlLocalMemorydServer)

	return &Server{config: cfg.Server, echo: e, logger: logger}, nil
}

// Handler exposes the complete HTTP surface for black-box tests and embedding.
func (s *Server) Handler() http.Handler { return s.echo }

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
		errorsFromServer <- s.echo.Start(s.config.Address)
	}()

	select {
	case err := <-errorsFromServer:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.logger.Info("HTTP server shutting down", "timeout", s.config.ShutdownTimeout)
	shutdownContext, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
	defer cancel()
	if err := s.echo.Shutdown(shutdownContext); err != nil {
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

func requestLogger(logger *slog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:    true,
		LogURIPath:   true,
		LogMethod:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogRemoteIP:  true,
		LogError:     true,
		LogValuesFunc: func(_ echo.Context, values middleware.RequestLoggerValues) error {
			level := slog.LevelInfo
			if values.Status >= http.StatusInternalServerError {
				level = slog.LevelError
			} else if values.Status >= http.StatusBadRequest {
				level = slog.LevelWarn
			}
			attrs := []slog.Attr{
				slog.String("request_id", values.RequestID),
				slog.String("method", values.Method),
				slog.String("path", values.URIPath),
				slog.Int("status", values.Status),
				slog.Duration("latency", values.Latency),
				slog.String("remote_address", values.RemoteIP),
			}
			if values.Error != nil {
				attrs = append(attrs, slog.Any("error", values.Error))
			}
			logger.LogAttrs(context.Background(), level, "HTTP request", attrs...)
			return nil
		},
	})
}
