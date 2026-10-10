package velad

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/version"
	"go.xchunk.org/vela/internal/wire"
)

const (
	shutdownTimeout     = 30 * time.Second
	janitorInitialDelay = time.Minute
	// blobCollectInterval: replaced and purged content is deleted this long
	// after its grace period at the latest, not only by the daily janitor.
	blobCollectInterval = 5 * time.Minute
)

func newServeCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server (default)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, env)
			if err != nil {
				return err
			}
			if err := cfg.RequireServe(); err != nil {
				return err
			}
			return serve(cmd.Context(), cfg, env)
		},
	}
	config.RegisterFlags(cmd.Flags(), true)
	return cmd
}

func serve(ctx context.Context, cfg *config.Config, env Env) error {
	log := cfg.Logger(env.Stderr)
	ver := version.Get()

	// Everything that can be misconfigured is checked before listening.
	theme, err := index.Load(cfg.ThemeDir)
	if err != nil {
		return fmt.Errorf("load theme: %w", err)
	}
	st, err := wire.OpenStorage(ctx, cfg.DataDir, log)
	if err != nil {
		return fmt.Errorf("open data directory %s: %w", cfg.DataDir, err)
	}
	defer func() { _ = st.Close() }()

	svc, err := wire.NewService(st, clock.System{}, log, service.Options{
		PublicURL:         cfg.PublicURL,
		ServerVersion:     ver.Version,
		MaxUpload:         cfg.MaxUpload,
		MaxCreate:         cfg.MaxCreate,
		LinkDefaultTTL:    cfg.LinkDefaultTTL,
		LinkMaxTTL:        cfg.LinkMaxTTL,
		LinkRetention:     cfg.LinkRetention,
		TrashRetention:    cfg.TrashRetention,
		DefaultVisibility: cfg.DefaultVisibility,
	})
	if err != nil {
		return err
	}
	if err := svc.EnsureSigningKey(ctx); err != nil {
		return fmt.Errorf("signing key: %w", err)
	}
	srv := server.New(svc, theme, log, server.Options{
		PublicURL:      cfg.PublicURL,
		SiteTitle:      cfg.SiteTitle,
		AccelPrefix:    cfg.AccelPrefix,
		TrustedProxies: cfg.TrustedProxies,
		RobotsAllow:    cfg.Robots == config.RobotsAllow,
		Version:        ver.Version,
		MaxCreateBytes: cfg.MaxCreate,
	})
	httpSrv := srv.HTTPServer(cfg.Listen)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if env.OnListen != nil {
		env.OnListen(ln.Addr())
	}
	delivery := "direct"
	if cfg.AccelEnabled() {
		delivery = "accel " + cfg.AccelPrefix
	}
	log.Info("velad started", "version", ver.Version, "commit", ver.Commit, "listen", ln.Addr().String(),
		"public_url", cfg.PublicURL, "data_dir", cfg.DataDir, "delivery", delivery)

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { svc.RunJanitorLoop(janitorCtx, janitorInitialDelay, cfg.JanitorInterval) })
	wg.Go(func() { svc.RunBlobCollectorLoop(janitorCtx, blobCollectInterval) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	select {
	case err := <-serveErr:
		stopJanitor()
		wg.Wait()
		return fmt.Errorf("serve: %w", err)
	case <-sigCtx.Done():
	}

	log.Info("shutting down", "timeout", shutdownTimeout)
	srv.SetDraining()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	stopJanitor()
	wg.Wait()
	if errors.Is(<-serveErr, http.ErrServerClosed) && err == nil {
		log.Info("velad stopped")
		return nil
	}
	return err
}
