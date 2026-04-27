package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aiminted/openai-proxy/internal/admin"
	"github.com/aiminted/openai-proxy/internal/config"
	"github.com/aiminted/openai-proxy/internal/keys"
	"github.com/aiminted/openai-proxy/internal/pricing"
	"github.com/aiminted/openai-proxy/internal/proxy"
	"github.com/aiminted/openai-proxy/internal/quota"
	"github.com/aiminted/openai-proxy/internal/ratelimit"
	"github.com/aiminted/openai-proxy/internal/store"
	"github.com/aiminted/openai-proxy/internal/usage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	st, err := store.Open(rootCtx, cfg.DatabaseURL, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(rootCtx); err != nil {
		return err
	}

	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return err
	}

	prc, err := pricing.Load(cfg.PricingPath)
	if err != nil {
		logger.Warn("pricing load failed; cost will be recorded as 0", "error", err)
		prc = pricing.Empty()
	}

	keySvc := keys.NewService(st.DB, st.Redis, cfg.KeyPrefix, cfg.VerifyCacheTTL)
	limiter := ratelimit.New(st.Redis)
	quotaSvc := quota.New(st.DB, st.Redis)
	recorder := usage.NewRecorder(st.DB)

	proxyHandler := proxy.New(proxy.Deps{
		Upstream:    upstream,
		UpstreamKey: cfg.UpstreamAPIKey,
		Keys:        keySvc,
		Limiter:     limiter,
		Quota:       quotaSvc,
		Pricing:     prc,
		Recorder:    recorder,
		Logger:      logger,
		Timeout:     cfg.StreamTimeout,
	})

	auth := admin.NewAuth(cfg.AdminPassword, cfg.SessionSecret, cfg.SessionTTL)
	api := admin.NewAPI(keySvc, st.DB, auth, cfg.CORSOrigins)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	mux.Handle("/v1/", proxyHandler)

	apiMux := http.NewServeMux()
	api.Mount(apiMux)
	mux.Handle("/admin/api/", api.CORS(apiMux))

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           withRequestLogging(logger, mux),
		ReadHeaderTimeout: 30 * time.Second,
	}

	go gracefulShutdown(rootCtx, rootCancel, server, logger)

	logger.Info("starting", "addr", cfg.ListenAddr, "upstream", upstream.String())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func gracefulShutdown(ctx context.Context, cancel context.CancelFunc, server *http.Server, logger *slog.Logger) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigChan:
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
	cancel()
}

func withRequestLogging(logger *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(sw, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(p)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

