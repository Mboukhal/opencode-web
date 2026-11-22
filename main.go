// Command opencodeweb serves a mobile-friendly web UI for OpenCode sessions
// started inside GitHub repositories cloned on this server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"opencodeweb/auth"
	"opencodeweb/config"
	"opencodeweb/oconfig"
	"opencodeweb/projects"
	"opencodeweb/runner"
	"opencodeweb/service"
	"opencodeweb/store"
	"opencodeweb/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	for _, dir := range []string{cfg.ProjectsDir, cfg.ConfigDir, cfg.StateDir, cfg.SkillsSrc} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := seedUser(context.Background(), log, st, cfg); err != nil {
		return err
	}

	home, _ := os.UserHomeDir()

	// The OpenCode config directory is shared by the server and every session:
	// write opencode.json and deploy local skills before the server starts so
	// it discovers them on first load.
	ocfg := &oconfig.Config{
		Bin: cfg.OpenCodeBin, ConfigDir: cfg.ConfigDir, StateDir: cfg.StateDir, Home: home,
		SkillsSrc: cfg.SkillsSrc, Model: cfg.OpenCodeModel, Agent: cfg.OpenCodeAgent,
		Providers: cfg.ProviderKeys, Log: log,
	}
	if err := ocfg.Ensure(context.Background()); err != nil {
		return fmt.Errorf("preparing opencode config: %w", err)
	}

	svc := service.New(service.Settings{
		Bin: cfg.OpenCodeBin, ConfigDir: cfg.ConfigDir, StateDir: cfg.StateDir, Home: home,
		Hostname: cfg.OpenCodeServiceHost, Port: cfg.OpenCodeServicePort,
		Password: cfg.OpenCodeServicePassword, Env: cfg.OpenCodeEnv, Log: log,
	})
	client, err := svc.Start(context.Background())
	if err != nil {
		// A server that failed to start must not take down the UI: the pages
		// report it and the user can still clone, push and pair later.
		log.Error("opencode server unavailable", "err", err)
	} else {
		svc.SaveAddress()
		if err := ocfg.PushKeys(context.Background(), client); err != nil {
			log.Error("storing provider keys", "err", err)
		}
	}

	rm := runner.New(svc.Client, cfg.OpenCodeTimeout)
	pm := projects.New(cfg.ProjectsDir, cfg.SSHKeyPath, st)
	pm.SetLogger(log)
	am := auth.New(cfg, st)

	srv, err := web.New(cfg, log, st, am, pm, rm, svc, ocfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				st.PurgeExpiredSessions(ctx)
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: the run event stream is long-lived.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "public", cfg.PublicBaseURL,
			"projects", cfg.ProjectsDir, "seed", cfg.SeedEmail())
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer svc.Stop()
		return httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}

// seedUser creates the single default user on first boot.
func seedUser(ctx context.Context, log *slog.Logger, st *store.Store, cfg *config.Config) error {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if n > 0 {
		return nil
	}
	email := cfg.SeedEmail()
	if _, err := st.UpsertUser(ctx, email, "Mohammed Boukhal", ""); err != nil {
		return fmt.Errorf("seeding user: %w", err)
	}
	log.Info("seeded default user", "email", email)
	return nil
}
