package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

type reconcileTrigger int

const (
	triggerStartup  reconcileTrigger = iota
	triggerReload
	triggerPeriodic
)

func configCandidates() []string {
	base := configDir
	if p := os.Getenv("SSH_GATEWAY_PROJECT"); p != "" {
		base = configDir + "/" + p
	}
	return []string{
		base + "/config.yaml",
		base + "/config.yml",
		base + "/config/config.yaml",
		base + "/config/config.yml",
	}
}

func findConfigPath() (string, error) {
	candidates := configCandidates()
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config file not found (tried: %s)", strings.Join(candidates, ", "))
}

func main() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			slog.Warn("invalid LOG_LEVEL, using info", "value", v)
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))

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

	// Set up endpoint logging before the initial reconcile so startup events
	// are captured. reconcile() re-loads the config; this early load is only
	// for endpoint initialisation.
	var initialEndpoint string
	var initialEW *endpointWriter
	if cfgPath, err := findConfigPath(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg.LogEndpoint != "" {
			if ew, err := newEndpointWriter(cfg.LogEndpoint); err != nil {
				slog.Warn("invalid log_endpoint, endpoint logging disabled", "err", err)
			} else {
				initialEW = ew
				initialEndpoint = cfg.LogEndpoint
				slog.SetDefault(slog.New(newMultiHandler(
					slog.NewTextHandler(os.Stdout, opts),
					slog.NewJSONHandler(ew, opts),
				)))
			}
		}
	}

	// Initial reconcile must complete before sshd starts.
	var initialInterval time.Duration
	var keepSshdConfig bool
	if cfg, err := reconcile(mgr, triggerStartup); err != nil {
		slog.Warn("initial reconcile failed", "err", err)
	} else {
		initialInterval = cfg.GetReconcileInterval()
		keepSshdConfig = cfg.KeepSshdConfig
		if cfg.LogEndpoint != initialEndpoint {
			if initialEW != nil {
				initialEW.close()
				initialEW = nil
			}
			initialEndpoint = ""
			if cfg.LogEndpoint != "" {
				if ew, err := newEndpointWriter(cfg.LogEndpoint); err != nil {
					slog.Warn("invalid log_endpoint, endpoint logging disabled", "err", err)
				} else {
					initialEW = ew
					initialEndpoint = cfg.LogEndpoint
					slog.SetDefault(slog.New(newMultiHandler(
						slog.NewTextHandler(os.Stdout, opts),
						slog.NewJSONHandler(ew, opts),
					)))
				}
			} else {
				slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
			}
		}
	}

	if err := sshd.WriteConfig(keepSshdConfig); err != nil {
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

	// Watch config for changes (handles atomic-rename writes from editors).
	// If config is found at startup, watch only its directory. Otherwise enter
	// discovery mode: watch all candidate directories until a config file appears,
	// then transition to watching only that file.
	resolvedPath, _ := findConfigPath()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("could not create watcher, falling back to SIGHUP only", "err", err)
	} else {
		var watcherReady bool
		if resolvedPath != "" {
			if err := watcher.Add(filepath.Dir(resolvedPath)); err != nil {
				slog.Warn("could not watch config dir, falling back to SIGHUP only", "dir", filepath.Dir(resolvedPath), "err", err)
				watcher.Close()
			} else {
				slog.Info("watching config for changes", "path", resolvedPath)
				watcherReady = true
			}
		} else {
			added := 0
			for _, p := range configCandidates() {
				if err := watcher.Add(filepath.Dir(p)); err == nil {
					added++
				}
			}
			if added == 0 {
				slog.Warn("could not watch any config dir, falling back to SIGHUP only")
				watcher.Close()
			} else {
				slog.Info("watching for config file (discovery mode)", "candidates", configCandidates())
				watcherReady = true
			}
		}

		if watcherReady {
			go func() {
				defer watcher.Close()
				current := resolvedPath
				for {
					select {
					case event, ok := <-watcher.Events:
						if !ok {
							return
						}
						if !(event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
							continue
						}
						base := filepath.Base(event.Name)
						if current == "" {
							// Discovery mode: wait for any config.yaml or config.yml.
							if base != "config.yaml" && base != "config.yml" {
								continue
							}
							p, err := findConfigPath()
							if err != nil {
								continue
							}
							// Transition: drop all candidate watches, lock onto resolved path.
							for _, c := range configCandidates() {
								_ = watcher.Remove(filepath.Dir(c))
							}
							if err := watcher.Add(filepath.Dir(p)); err != nil {
								slog.Warn("could not watch config dir after discovery", "dir", filepath.Dir(p), "err", err)
								return
							}
							current = p
							slog.Info("config file found, watching", "path", current)
						} else if base != filepath.Base(current) {
							continue
						}
						slog.Info("config file changed, triggering reconcile")
						trigger()
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
			curEndpoint = initialEndpoint
			curEW       = initialEW
		)
		if curInterval > 0 {
			slog.Info("periodic reconcile enabled", "interval", curInterval)
			ticker = time.NewTicker(curInterval)
			tickerCh = ticker.C
		}

		runReconcile := func(trigger reconcileTrigger) {
			cfg, err := reconcile(mgr, trigger)
			if err != nil {
				slog.Warn("reconcile failed", "err", err)
				return
			}

			if interval := cfg.GetReconcileInterval(); interval != curInterval {
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

			if cfg.LogEndpoint != curEndpoint {
				if curEW != nil {
					curEW.close()
					curEW = nil
				}
				var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
				if cfg.LogEndpoint != "" {
					ew, err := newEndpointWriter(cfg.LogEndpoint)
					if err != nil {
						slog.Warn("invalid log_endpoint, endpoint logging disabled", "err", err)
					} else {
						curEW = ew
						handler = newMultiHandler(
							slog.NewTextHandler(os.Stdout, opts),
							slog.NewJSONHandler(ew, opts),
						)
					}
				}
				slog.SetDefault(slog.New(handler))
				curEndpoint = cfg.LogEndpoint
			}
		}

		for {
			select {
			case <-reconcileCh:
				// Drain any follow-on triggers that arrive within the debounce
				// window (e.g. truncate + write from a single file save).
				timer := time.NewTimer(200 * time.Millisecond)
			debounce:
				for {
					select {
					case <-reconcileCh:
						timer.Reset(200 * time.Millisecond)
					case <-timer.C:
						break debounce
					}
				}
				runReconcile(triggerReload)
			case <-tickerCh:
				runReconcile(triggerPeriodic)
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

func reconcile(mgr *usermgr.Manager, trigger reconcileTrigger) (*config.Config, error) {
	path, err := findConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	fetch := trigger == triggerPeriodic || cfg.FetchKeysOnReload

	var existing map[string][]string
	if !fetch {
		users, err := mgr.ListUsers()
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		existing = mgr.ReadAnnotatedKeys(users)
	}

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	logger := slog.Default().With("event_id", hex.EncodeToString(b), "trigger", triggerName(trigger), "project", cfg.Project)
	logger.Info("reconcile", "phase", "start", "users", len(cfg.Users), "fetch", fetch)

	keys, err := cfg.ResolveKeys(fetch, existing)
	if err != nil {
		return nil, err
	}
	changes, err := mgr.Reconcile(keys, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("reconcile", "phase", "end", "changes", changes)
	return cfg, nil
}

func triggerName(t reconcileTrigger) string {
	switch t {
	case triggerStartup:
		return "startup"
	case triggerReload:
		return "reload"
	case triggerPeriodic:
		return "periodic"
	default:
		return "unknown"
	}
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
