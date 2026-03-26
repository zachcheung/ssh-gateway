package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zachcheung/ssh-gateway/internal/config"
	"github.com/zachcheung/ssh-gateway/internal/sshd"
	"github.com/zachcheung/ssh-gateway/internal/usermgr"
)

const configPath = "/etc/ssh-gateway/config.yaml"

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	mgr := usermgr.New()

	if err := reconcile(mgr); err != nil {
		log.Printf("initial reconcile: %v (waiting for SIGHUP)", err)
	}

	if err := sshd.GenerateHostKeys(); err != nil {
		log.Fatalf("generate host keys: %v", err)
	}

	proc, err := sshd.Start()
	if err != nil {
		log.Fatalf("start sshd: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- proc.Wait()
	}()

	// PID 1 zombie reaper
	go reapChildren()

	for {
		select {
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				log.Println("SIGHUP received, reloading config")
				if err := reconcile(mgr); err != nil {
					log.Printf("reconcile failed: %v", err)
					continue
				}
			case syscall.SIGTERM, syscall.SIGINT:
				log.Println("shutting down")
				if err := proc.Stop(); err != nil {
					log.Printf("stop sshd: %v", err)
				}
				os.Exit(0)
			}
		case err := <-waitCh:
			if err != nil {
				log.Fatalf("sshd exited: %v", err)
			}
			log.Fatal("sshd exited unexpectedly")
		}
	}
}

func reconcile(mgr *usermgr.Manager) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log.Printf("reconciling project %q (%d users)", cfg.Project, len(cfg.Users))
	return mgr.Reconcile(cfg.UserMap())
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
