package sshd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
)

const (
	sshdBin    = "/usr/sbin/sshd"
	configPath = "/etc/ssh/sshd_config"
	hostKeyDir = "/etc/ssh"
)

type Process struct {
	cmd *exec.Cmd
}

func GenerateHostKeys() error {
	types := []string{"rsa", "ecdsa", "ed25519"}
	for _, t := range types {
		keyPath := fmt.Sprintf("%s/ssh_host_%s_key", hostKeyDir, t)
		if _, err := os.Stat(keyPath); err == nil {
			continue
		}
		log.Printf("generating %s host key", t)
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
	cmd := exec.Command(sshdBin, "-D", "-e")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sshd: %w", err)
	}

	log.Printf("sshd started (pid %d)", cmd.Process.Pid)
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
