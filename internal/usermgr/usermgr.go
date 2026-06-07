package usermgr

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
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

func (m *Manager) EnsureGroup() error {
	return m.ensureGroup(managedGroup)
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

func (m *Manager) Reconcile(desired map[string][]string, logger *slog.Logger) (int, error) {
	if err := m.ensureGroup(managedGroup); err != nil {
		return 0, fmt.Errorf("ensure group: %w", err)
	}

	current, err := m.ListUsers()
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}

	var changes int

	for name := range current {
		if _, ok := desired[name]; !ok {
			logger.Debug("removing user", "user", name)
			if err := m.removeUser(name); err != nil {
				logger.Warn("remove user failed", "user", name, "err", err)
				continue
			}
			logger.Info("user removed", "user", name)
			changes++
		}
	}

	for name, keys := range desired {
		var oldKeys []string
		isNew := !current[name]
		if isNew {
			logger.Debug("adding user", "user", name)
			if err := m.addUser(name); err != nil {
				logger.Warn("add user failed", "user", name, "err", err)
				continue
			}
			logger.Info("user added", "user", name)
			changes++
		} else {
			oldKeys, _ = m.readAuthorizedKeys(name)
		}
		if countKeys(keys) == 0 {
			logger.Warn("user has no keys, access denied", "user", name)
		}
		if err := m.writeAuthorizedKeys(name, keys); err != nil {
			logger.Warn("write keys failed", "user", name, "err", err)
			continue
		}
		if isNew {
			for line, ak := range keysBySource(keys) {
				logger.Info("key added", "user", name, "source", ak.source, "fingerprint", keyFingerprint(line))
			}
		} else if !keysEqual(oldKeys, keys) {
			oldSet := keysBySource(oldKeys)
			newSet := keysBySource(keys)
			for line, ak := range newSet {
				if _, exists := oldSet[line]; !exists {
					logger.Info("key added", "user", name, "source", ak.source, "fingerprint", keyFingerprint(line))
				}
			}
			for line, ak := range oldSet {
				if _, exists := newSet[line]; !exists {
					logger.Info("key removed", "user", name, "source", ak.source, "fingerprint", keyFingerprint(line))
				}
			}
			changes++
		}
	}

	return changes, nil
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

func (m *Manager) nextGID() (int, error) {
	f, err := os.Open(m.groupPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	maxGID := minUID - 1
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 4)
		if len(fields) < 3 {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if gid > maxGID {
			maxGID = gid
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return maxGID + 1, nil
}

func (m *Manager) addUser(name string) error {
	uid, err := m.nextUID()
	if err != nil {
		return fmt.Errorf("next uid: %w", err)
	}
	gid, err := m.nextGID()
	if err != nil {
		return fmt.Errorf("next gid: %w", err)
	}

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
	home := filepath.Join(m.homeBase, name)
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("remove home: %w", err)
	}

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

	return nil
}

func (m *Manager) readAuthorizedKeys(name string) ([]string, error) {
	akPath := filepath.Join(m.homeBase, name, ".ssh", "authorized_keys")
	data, err := os.ReadFile(akPath)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Skip blank lines and non-marker comments (e.g. the managed-by header).
		// ResolveKeys output only contains marker comments, so these must match.
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "# ssh-gateway:source=")) {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// ReadAnnotatedKeys reads the raw authorized_keys content (including source
// marker comments) for each user in the provided set. Missing files are
// silently skipped.
func (m *Manager) ReadAnnotatedKeys(users map[string]bool) map[string][]string {
	result := make(map[string][]string, len(users))
	for name := range users {
		lines, err := m.readAuthorizedKeys(name)
		if err == nil {
			result[name] = lines
		}
	}
	return result
}

// countKeys returns the number of non-comment lines (actual key entries).
func countKeys(lines []string) int {
	n := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// keyFingerprint returns the SHA256 fingerprint of an authorized_keys line
// in the same format sshd uses (e.g. "SHA256:abc123..."), allowing log
// entries to be correlated with sshd's "Accepted publickey" lines.
// Falls back to the raw line if parsing fails.
func keyFingerprint(line string) string {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return line
	}
	return ssh.FingerprintSHA256(pk)
}

type sourceKey struct {
	line   string
	source string
}

// keysBySource parses an annotated lines slice and returns a map of key line →
// sourceKey, carrying the source marker that preceded each key in the file.
func keysBySource(lines []string) map[string]sourceKey {
	result := map[string]sourceKey{}
	source := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "# ssh-gateway:source=") {
			source = strings.TrimPrefix(l, "# ssh-gateway:source=")
		} else if !strings.HasPrefix(l, "#") && l != "" {
			result[l] = sourceKey{line: l, source: source}
		}
	}
	return result
}


func keysEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *Manager) writeAuthorizedKeys(name string, keys []string) error {
	home := filepath.Join(m.homeBase, name)
	sshDir := filepath.Join(home, ".ssh")
	akPath := filepath.Join(sshDir, "authorized_keys")

	uid, gid, err := m.lookupUID(name)
	if err != nil {
		return fmt.Errorf("lookup uid: %w", err)
	}
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("mkdir .ssh: %w", err)
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return fmt.Errorf("chown .ssh: %w", err)
	}

	var content []byte
	if len(keys) > 0 {
		content = []byte("# This file is managed by ssh-gateway. Do not edit manually.\n" + strings.Join(keys, "\n") + "\n")
	}
	if err := os.WriteFile(akPath, content, 0600); err != nil {
		return err
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
	gid, err := m.nextGID()
	if err != nil {
		return err
	}
	return m.appendLine(m.groupPath, fmt.Sprintf("%s:x:%d:", group, gid))
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
