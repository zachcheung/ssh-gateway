package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/zachcheung/ssh-gateway/internal/keyfetch"
	"go.yaml.in/yaml/v4"
)

var providerShorthands = map[string]string{
	"github": "https://github.com",
	"gitlab": "https://gitlab.com",
}

var validKeyTypes = map[string]bool{
	"ecdsa":      true,
	"ecdsa-sk":   true,
	"ed25519":    true,
	"ed25519-sk": true,
	"rsa":        true,
}

var sshPrefixToType = map[string]string{
	"ecdsa-sha2-nistp256":                 "ecdsa",
	"ecdsa-sha2-nistp384":                 "ecdsa",
	"ecdsa-sha2-nistp521":                 "ecdsa",
	"sk-ecdsa-sha2-nistp256@openssh.com":  "ecdsa-sk",
	"ssh-ed25519":                         "ed25519",
	"sk-ssh-ed25519@openssh.com":          "ed25519-sk",
	"ssh-rsa":                             "rsa",
}

type KeyTypes struct {
	Allowed    []string `yaml:"allowed"`
	Disallowed []string `yaml:"disallowed"`
}

type User struct {
	Name string   `yaml:"name"`
	Keys []string `yaml:"keys"`
}

type Config struct {
	Project           string   `yaml:"project"`
	KeyProvider       string   `yaml:"key_provider"`
	KeyTypes          KeyTypes `yaml:"key_types"`
	ReconcileInterval string   `yaml:"reconcile_interval"`
	Users             []User   `yaml:"users"`
}

// GetReconcileInterval returns the parsed interval, or 0 if not set.
// The value is already validated by Load, so parsing cannot fail here.
func (c *Config) GetReconcileInterval() time.Duration {
	if c.ReconcileInterval == "" {
		return 0
	}
	d, _ := time.ParseDuration(c.ReconcileInterval)
	return d
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) ProviderURL() string {
	if url, ok := providerShorthands[c.KeyProvider]; ok {
		return url
	}
	return c.KeyProvider
}

func (c *Config) validate() error {
	if c.Project == "" {
		return fmt.Errorf("project name is required")
	}

	for _, t := range c.KeyTypes.Allowed {
		if !validKeyTypes[t] {
			return fmt.Errorf("key_types.allowed: unknown type %q", t)
		}
	}
	for _, t := range c.KeyTypes.Disallowed {
		if !validKeyTypes[t] {
			return fmt.Errorf("key_types.disallowed: unknown type %q", t)
		}
	}
	if len(c.KeyTypes.Allowed) > 0 && len(c.KeyTypes.Disallowed) > 0 {
		slog.Warn("both key_types.allowed and key_types.disallowed set, using allowed only")
		c.KeyTypes.Disallowed = nil
	}

	if c.ReconcileInterval != "" {
		d, err := time.ParseDuration(c.ReconcileInterval)
		if err != nil {
			return fmt.Errorf("reconcile_interval: %w", err)
		}
		if d < 5*time.Second {
			return fmt.Errorf("reconcile_interval: minimum is 5s, got %s", c.ReconcileInterval)
		}
	}

	seen := make(map[string]bool)
	for i, u := range c.Users {
		if u.Name == "" {
			return fmt.Errorf("users[%d]: name is required", i)
		}
		if seen[u.Name] {
			return fmt.Errorf("users[%d]: duplicate user %q", i, u.Name)
		}
		seen[u.Name] = true

		if len(u.Keys) == 0 && c.KeyProvider == "" {
			return fmt.Errorf("user %q: keys required (or set key_provider)", u.Name)
		}
		for j, k := range u.Keys {
			k = strings.TrimSpace(k)
			if k == "" {
				return fmt.Errorf("user %q: keys[%d] is empty", u.Name, j)
			}
		}
	}

	return nil
}

// sshKeys drops lines that don't start with a recognised SSH public key type,
// warning for each rejected line so misconfigured URLs (e.g. auth redirects
// returning HTML) are visible in the logs.
func sshKeys(keys []string, user string) []string {
	var valid []string
	for _, k := range keys {
		prefix := strings.SplitN(k, " ", 2)[0]
		if _, ok := sshPrefixToType[prefix]; ok {
			valid = append(valid, k)
		} else {
			slog.Warn("dropping invalid key line", "user", user, "prefix", prefix)
		}
	}
	return valid
}

func uniqueKeys(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	var result []string
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			result = append(result, k)
		}
	}
	return result
}

func (c *Config) filterKeys(keys []string) []string {
	if len(c.KeyTypes.Allowed) == 0 && len(c.KeyTypes.Disallowed) == 0 {
		return keys
	}

	var allowed map[string]bool
	if len(c.KeyTypes.Allowed) > 0 {
		allowed = make(map[string]bool, len(c.KeyTypes.Allowed))
		for _, t := range c.KeyTypes.Allowed {
			allowed[t] = true
		}
	}

	var disallowed map[string]bool
	if len(c.KeyTypes.Disallowed) > 0 {
		disallowed = make(map[string]bool, len(c.KeyTypes.Disallowed))
		for _, t := range c.KeyTypes.Disallowed {
			disallowed[t] = true
		}
	}

	var filtered []string
	for _, k := range keys {
		prefix := strings.SplitN(k, " ", 2)[0]
		kt, ok := sshPrefixToType[prefix]
		if !ok {
			filtered = append(filtered, k)
			continue
		}
		if allowed != nil && !allowed[kt] {
			continue
		}
		if disallowed != nil && disallowed[kt] {
			continue
		}
		filtered = append(filtered, k)
	}
	return filtered
}

func (c *Config) ResolveKeys() (map[string][]string, error) {
	provider := c.ProviderURL()
	m := make(map[string][]string, len(c.Users))

	for _, u := range c.Users {
		var keys []string

		if len(u.Keys) == 0 {
			url := provider + "/" + u.Name + ".keys"
			fetched, err := keyfetch.Fetch(url)
			if err != nil {
				return nil, fmt.Errorf("user %q: %w", u.Name, err)
			}
			keys = fetched
		} else {
			for _, k := range u.Keys {
				k = strings.TrimSpace(k)
				if keyfetch.IsURL(k) {
					fetched, err := keyfetch.Fetch(k)
					if err != nil {
						return nil, fmt.Errorf("user %q: %w", u.Name, err)
					}
					keys = append(keys, fetched...)
				} else {
					keys = append(keys, k)
				}
			}
		}

		keys = sshKeys(keys, u.Name)
		keys = c.filterKeys(keys)
		keys = uniqueKeys(keys)
		if len(keys) == 0 {
			slog.Warn("all keys filtered by key_types", "user", u.Name)
		}

		m[u.Name] = keys
	}

	return m, nil
}
