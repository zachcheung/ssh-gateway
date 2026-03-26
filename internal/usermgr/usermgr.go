package usermgr

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	minUID       = 1000
	passwdFile   = "/etc/passwd"
	shadowFile   = "/etc/shadow"
	groupFile    = "/etc/group"
	defaultShell = "/bin/false"
	homeBase     = "/home"
	managedGroup = "ssh-gateway"
)

type Manager struct {
	passwdPath string
	shadowPath string
	groupPath  string
	homeBase   string
}

func New() *Manager {
	return &Manager{
		passwdPath: passwdFile,
		shadowPath: shadowFile,
		groupPath:  groupFile,
		homeBase:   homeBase,
	}
}

func (m *Manager) ListUsers() (map[string]bool, error) {
	members, err := m.groupMembers(managedGroup)
	if err != nil {
		return nil, err
	}
	users := make(map[string]bool, len(members))
	for _, name := range members {
		users[name] = true
	}
	return users, nil
}

func (m *Manager) groupMembers(group string) ([]string, error) {
	f, err := os.Open(m.groupPath)
	if err != nil {
		return nil, fmt.Errorf("open group: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 4)
		if len(fields) < 4 || fields[0] != group {
			continue
		}
		if fields[3] == "" {
			return nil, nil
		}
		return strings.Split(fields[3], ","), nil
	}
	return nil, scanner.Err()
}

func (m *Manager) Reconcile(desired map[string][]string) error {
	if err := m.ensureGroup(managedGroup); err != nil {
		return fmt.Errorf("ensure group: %w", err)
	}

	current, err := m.ListUsers()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	for name := range current {
		if _, ok := desired[name]; !ok {
			log.Printf("removing user %q", name)
			if err := m.removeUser(name); err != nil {
				return fmt.Errorf("remove user %q: %w", name, err)
			}
		}
	}

	for name, keys := range desired {
		if !current[name] {
			log.Printf("adding user %q", name)
			if err := m.addUser(name); err != nil {
				return fmt.Errorf("add user %q: %w", name, err)
			}
		}
		if err := m.writeAuthorizedKeys(name, keys); err != nil {
			return fmt.Errorf("write keys for %q: %w", name, err)
		}
	}

	return nil
}

func (m *Manager) nextUID() (int, error) {
	f, err := os.Open(m.passwdPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	maxUID := minUID - 1
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 7)
		if len(fields) < 3 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if uid > maxUID {
			maxUID = uid
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return maxUID + 1, nil
}

func (m *Manager) addUser(name string) error {
	uid, err := m.nextUID()
	if err != nil {
		return fmt.Errorf("next uid: %w", err)
	}
	gid := uid

	if err := m.appendLine(m.groupPath, fmt.Sprintf("%s:x:%d:", name, gid)); err != nil {
		return fmt.Errorf("append group: %w", err)
	}
	if err := m.appendLine(m.passwdPath, fmt.Sprintf("%s:x:%d:%d::%s/%s:%s", name, uid, gid, m.homeBase, name, defaultShell)); err != nil {
		return fmt.Errorf("append passwd: %w", err)
	}
	if err := m.appendLine(m.shadowPath, fmt.Sprintf("%s:*:::::::", name)); err != nil {
		return fmt.Errorf("append shadow: %w", err)
	}
	if err := m.addGroupMember(managedGroup, name); err != nil {
		return fmt.Errorf("add to managed group: %w", err)
	}

	home := filepath.Join(m.homeBase, name)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("mkdir ssh: %w", err)
	}
	if err := os.Chown(home, uid, gid); err != nil {
		return fmt.Errorf("chown home: %w", err)
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return fmt.Errorf("chown ssh: %w", err)
	}

	return nil
}

func (m *Manager) removeUser(name string) error {
	if err := m.removeGroupMember(managedGroup, name); err != nil {
		return fmt.Errorf("remove from managed group: %w", err)
	}
	if err := m.removeLine(m.passwdPath, name+":"); err != nil {
		return fmt.Errorf("remove from passwd: %w", err)
	}
	if err := m.removeLine(m.shadowPath, name+":"); err != nil {
		return fmt.Errorf("remove from shadow: %w", err)
	}
	if err := m.removeLine(m.groupPath, name+":"); err != nil {
		return fmt.Errorf("remove from group: %w", err)
	}

	home := filepath.Join(m.homeBase, name)
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("remove home: %w", err)
	}

	return nil
}

func (m *Manager) writeAuthorizedKeys(name string, keys []string) error {
	home := filepath.Join(m.homeBase, name)
	sshDir := filepath.Join(home, ".ssh")
	akPath := filepath.Join(sshDir, "authorized_keys")

	content := strings.Join(keys, "\n") + "\n"
	if err := os.WriteFile(akPath, []byte(content), 0600); err != nil {
		return err
	}

	uid, gid, err := m.lookupUID(name)
	if err != nil {
		return fmt.Errorf("lookup uid: %w", err)
	}
	return os.Chown(akPath, uid, gid)
}

func (m *Manager) lookupUID(name string) (int, int, error) {
	f, err := os.Open(m.passwdPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 7)
		if len(fields) < 4 || fields[0] != name {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, 0, fmt.Errorf("parse uid: %w", err)
		}
		gid, err := strconv.Atoi(fields[3])
		if err != nil {
			return 0, 0, fmt.Errorf("parse gid: %w", err)
		}
		return uid, gid, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, fmt.Errorf("user %q not found", name)
}

func (m *Manager) appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

func (m *Manager) ensureGroup(group string) error {
	f, err := os.Open(m.groupPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 4)
		if len(fields) >= 1 && fields[0] == group {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return m.appendLine(m.groupPath, fmt.Sprintf("%s:x:%d:", group, minUID-1))
}

func (m *Manager) addGroupMember(group, user string) error {
	return m.updateGroupMembers(group, func(members []string) []string {
		return append(members, user)
	})
}

func (m *Manager) removeGroupMember(group, user string) error {
	return m.updateGroupMembers(group, func(members []string) []string {
		var result []string
		for _, m := range members {
			if m != user {
				result = append(result, m)
			}
		}
		return result
	})
}

func (m *Manager) updateGroupMembers(group string, fn func([]string) []string) error {
	data, err := os.ReadFile(m.groupPath)
	if err != nil {
		return err
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(line, ":", 4)
		if len(fields) >= 4 && fields[0] == group {
			var members []string
			if fields[3] != "" {
				members = strings.Split(fields[3], ",")
			}
			members = fn(members)
			fields[3] = strings.Join(members, ",")
			line = strings.Join(fields, ":")
		}
		lines = append(lines, line)
	}

	return os.WriteFile(m.groupPath, []byte(strings.Join(lines, "\n")), 0644)
}

func (m *Manager) removeLine(path, prefix string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}
