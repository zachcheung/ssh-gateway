package sshd

import (
	_ "embed"
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

const (
	sshdBin      = "/usr/sbin/sshd"
	hostKeyDir   = "/etc/ssh"
	sshdConfPath = "/etc/ssh/sshd_config"
)

//go:embed sshd_config
var defaultConfig []byte

type Process struct {
	cmd *exec.Cmd
}

// WriteConfig writes the embedded sshd_config to sshdConfPath on first run.
// If keepExisting is true and the file exists, it is left untouched regardless
// of content — the operator owns that file.
// Otherwise, if the file matches the built-in it is left untouched; if it
// differs a warning is logged and the file is replaced with the current built-in.
func WriteConfig(keepExisting bool) error {
	existing, err := os.ReadFile(sshdConfPath)
	if err == nil {
		if keepExisting {
			slog.Info("sshd_config exists, keeping as-is (keep_sshd_config=true)", "path", sshdConfPath)
			return nil
		}
		if bytes.Equal(existing, defaultConfig) {
			slog.Info("sshd_config unchanged", "path", sshdConfPath)
			return nil
		}
		slog.Warn("sshd_config differs from built-in, replacing", "path", sshdConfPath)
	}
	slog.Info("writing sshd_config", "path", sshdConfPath)
	return os.WriteFile(sshdConfPath, defaultConfig, 0644)
}

func GenerateHostKeys() error {
	types := []string{"rsa", "ecdsa", "ed25519"}
	for _, t := range types {
		keyPath := fmt.Sprintf("%s/ssh_host_%s_key", hostKeyDir, t)
		if info, err := os.Stat(keyPath); err == nil {
			if mode := info.Mode().Perm(); mode != 0600 {
				slog.Warn("host key has unsafe permissions, fixing", "path", keyPath, "mode", mode)
				if err := os.Chmod(keyPath, 0600); err != nil {
					return fmt.Errorf("chmod %s key: %w", t, err)
				}
			}
			continue
		}
		slog.Info("generating host key", "type", t)
		cmd := exec.Command("ssh-keygen", "-t", t, "-f", keyPath, "-N", "")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("generate %s key: %w", t, err)
		}
	}
	return nil
}

func Start() (*Process, error) {
	cmd := exec.Command(sshdBin, "-D", "-e", "-f", sshdConfPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sshd: %w", err)
	}

	slog.Info("sshd started", "pid", cmd.Process.Pid)
	return &Process{cmd: cmd}, nil
}

func (p *Process) Wait() error {
	return p.cmd.Wait()
}

func (p *Process) Signal(sig os.Signal) error {
	return p.cmd.Process.Signal(sig)
}

func (p *Process) Stop() error {
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	return p.cmd.Wait()
}
