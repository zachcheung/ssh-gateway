package main

import (
	"log"
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

const configDir = "/etc/ssh-gateway"

func configPath() string {
	if p := os.Getenv("SSH_GATEWAY_PROJECT"); p != "" {
		return configDir + "/" + p + "/config.yaml"
	}
	return configDir + "/config.yaml"
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	data, err := os.ReadFile("/etc/os-release")
	if err != nil || !strings.Contains(string(data), "ID=alpine") {
		log.Println("WARNING: not Alpine Linux, this tool is designed for containers only")
	}

	mgr := usermgr.New()

	// Initial reconcile must complete before sshd starts.
	var initialInterval time.Duration
	if cfg, err := reconcile(mgr); err != nil {
		log.Printf("initial reconcile: %v", err)
	} else {
		initialInterval = cfg.GetReconcileInterval()
	}

	if err := sshd.GenerateHostKeys(); err != nil {
		log.Fatalf("generate host keys: %v", err)
	}

	proc, err := sshd.Start()
	if err != nil {
		log.Fatalf("start sshd: %v", err)
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
		log.Printf("WARNING: could not create watcher: %v (SIGHUP only)", err)
	} else {
		watchDir := filepath.Dir(cfgPath)
		if err := watcher.Add(watchDir); err != nil {
			log.Printf("WARNING: could not watch %s: %v (SIGHUP only)", watchDir, err)
			watcher.Close()
		} else {
			log.Printf("watching %s for changes", cfgPath)
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
							log.Println("config file changed, triggering reconcile")
							trigger()
						}
					case err, ok := <-watcher.Errors:
						if !ok {
							return
						}
						log.Printf("watcher error: %v", err)
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
				log.Println("SIGHUP received, triggering reconcile")
				trigger()
			case syscall.SIGTERM, syscall.SIGINT:
				log.Println("shutting down")
				if err := proc.Stop(); err != nil {
					log.Printf("stop sshd: %v", err)
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
			log.Printf("periodic reconcile enabled: every %s", curInterval)
			ticker = time.NewTicker(curInterval)
			tickerCh = ticker.C
		}

		runReconcile := func() {
			cfg, err := reconcile(mgr)
			if err != nil {
				log.Printf("reconcile failed: %v", err)
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
				log.Printf("periodic reconcile enabled: every %s", interval)
				ticker = time.NewTicker(interval)
				tickerCh = ticker.C
			} else {
				log.Println("periodic reconcile disabled")
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
		log.Fatalf("sshd exited: %v", err)
	}
	log.Fatal("sshd exited unexpectedly")
}

func reconcile(mgr *usermgr.Manager) (*config.Config, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}

	log.Printf("reconciling project %q (%d users)", cfg.Project, len(cfg.Users))

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
		log.Printf("reaped zombie pid %d", pid)
	}
}
