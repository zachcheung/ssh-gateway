package main

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/zachcheung/ssh-gateway/internal/config"
	"github.com/zachcheung/ssh-gateway/internal/sshd"
	"github.com/zachcheung/ssh-gateway/internal/usermgr"
)

// version is set at build time via -ldflags "-X main.version=v1.2.3".
var version = "HEAD"

const configDir = "/etc/ssh-gateway"

func configPath() string {
	if p := os.Getenv("SSH_GATEWAY_PROJECT"); p != "" {
		return configDir + "/" + p + "/config.yaml"
	}
	return configDir + "/config.yaml"
}

func main() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			slog.Warn("invalid LOG_LEVEL, using info", "value", v)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	slog.Info("starting", "version", version)

	data, err := os.ReadFile("/etc/os-release")
	if err != nil || !strings.Contains(string(data), "ID=alpine") {
		slog.Warn("not Alpine Linux, this tool is designed for containers only")
	}

	mgr := usermgr.New()

	// Ensure the ssh-gateway group exists before sshd starts.
	// AllowGroups ssh-gateway requires the group to be present at startup
	// even if the initial reconcile fails (e.g. config not yet mounted).
	if err := mgr.EnsureGroup(); err != nil {
		slog.Error("ensure group", "err", err)
		os.Exit(1)
	}

	// Initial reconcile must complete before sshd starts.
	var initialInterval time.Duration
	if cfg, err := reconcile(mgr); err != nil {
		slog.Warn("initial reconcile failed", "err", err)
	} else {
		initialInterval = cfg.GetReconcileInterval()
	}

	if err := sshd.WriteConfig(); err != nil {
		slog.Error("write default sshd_config", "err", err)
		os.Exit(1)
	}

	if err := sshd.GenerateHostKeys(); err != nil {
		slog.Error("generate host keys", "err", err)
		os.Exit(1)
	}

	proc, err := sshd.Start()
	if err != nil {
		slog.Error("start sshd", "err", err)
		os.Exit(1)
	}

	// reconcileCh is buffered 1: multiple simultaneous triggers collapse into one pending reconcile.
	reconcileCh := make(chan struct{}, 1)
	trigger := func() {
		select {
		case reconcileCh <- struct{}{}:
		default:
		}
	}

	// Watch config directory for file changes (handles atomic-rename writes from editors).
	cfgPath := configPath()
	cfgBase := filepath.Base(cfgPath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("could not create watcher, falling back to SIGHUP only", "err", err)
	} else {
		watchDir := filepath.Dir(cfgPath)
		if err := watcher.Add(watchDir); err != nil {
			slog.Warn("could not watch config dir, falling back to SIGHUP only", "dir", watchDir, "err", err)
			watcher.Close()
		} else {
			slog.Info("watching config for changes", "path", cfgPath)
			go func() {
				defer watcher.Close()
				for {
					select {
					case event, ok := <-watcher.Events:
						if !ok {
							return
						}
						if filepath.Base(event.Name) == cfgBase &&
							(event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
							slog.Info("config file changed, triggering reconcile")
							trigger()
						}
					case err, ok := <-watcher.Errors:
						if !ok {
							return
						}
						slog.Warn("watcher error", "err", err)
					}
				}
			}()
		}
	}

	// SIGHUP now just triggers the unified reconcile channel.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGHUP:
				slog.Info("SIGHUP received, triggering reconcile")
				trigger()
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("shutting down")
				if err := proc.Stop(); err != nil {
					slog.Warn("stop sshd", "err", err)
				}
				os.Exit(0)
			}
		}
	}()

	go reapChildren()

	// Reconcile loop: drains reconcileCh and manages the optional periodic ticker.
	// All three trigger sources (fsnotify, SIGHUP, ticker) funnel through reconcileCh,
	// so at most one reconcile is ever queued at a time.
	go func() {
		var (
			ticker      *time.Ticker
			tickerCh    <-chan time.Time
			curInterval = initialInterval
		)
		if curInterval > 0 {
			slog.Info("periodic reconcile enabled", "interval", curInterval)
			ticker = time.NewTicker(curInterval)
			tickerCh = ticker.C
		}

		runReconcile := func() {
			cfg, err := reconcile(mgr)
			if err != nil {
				slog.Warn("reconcile failed", "err", err)
				return
			}
			interval := cfg.GetReconcileInterval()
			if interval == curInterval {
				return
			}
			if ticker != nil {
				ticker.Stop()
				ticker = nil
				tickerCh = nil
			}
			if interval > 0 {
				slog.Info("periodic reconcile enabled", "interval", interval)
				ticker = time.NewTicker(interval)
				tickerCh = ticker.C
			} else {
				slog.Info("periodic reconcile disabled")
			}
			curInterval = interval
		}

		for {
			select {
			case <-reconcileCh:
				runReconcile()
			case <-tickerCh:
				runReconcile()
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- proc.Wait()
	}()

	if err := <-waitCh; err != nil {
		slog.Error("sshd exited", "err", err)
		os.Exit(1)
	}
	slog.Error("sshd exited unexpectedly")
	os.Exit(1)
}

func reconcile(mgr *usermgr.Manager) (*config.Config, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}

	slog.Debug("reconciling", "project", cfg.Project, "users", len(cfg.Users))

	keys, err := cfg.ResolveKeys()
	if err != nil {
		return nil, err
	}
	return cfg, mgr.Reconcile(keys)
}

func reapChildren() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			return
		}
		slog.Debug("reaped zombie", "pid", pid)
	}
}
