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
	Project            string   `yaml:"project"`
	KeyProvider        string   `yaml:"key_provider"`
	KeyTypes           KeyTypes `yaml:"key_types"`
	ReconcileInterval  string   `yaml:"reconcile_interval"`
	FetchKeysOnReload  bool     `yaml:"fetch_keys_on_reload"`
	Users              []User   `yaml:"users"`
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

// keyType scans the tokens in an authorized_keys line for a recognised SSH key
// type, handling optional leading options (e.g. "no-pty ssh-ed25519 ...").
func keyType(line string) (string, bool) {
	for _, token := range strings.Fields(line) {
		if kt, ok := sshPrefixToType[token]; ok {
			return kt, true
		}
	}
	return "", false
}

// sshKeys drops lines with no recognised SSH key type, warning for each so
// misconfigured URLs (e.g. auth redirects returning HTML) are visible in logs.
func sshKeys(keys []string, user string) []string {
	var valid []string
	for _, k := range keys {
		if _, ok := keyType(k); ok {
			valid = append(valid, k)
		} else {
			slog.Warn("dropping invalid key line", "user", user, "line", k)
		}
	}
	return valid
}

// Source annotation markers embedded as comments in authorized_keys.
// sshd ignores comment lines, so markers are invisible to the SSH server.
const (
	markerPrefix = "# ssh-gateway:source="
	markerInline = markerPrefix + "inline"
)

func markerForURL(url string) string      { return markerPrefix + "url:" + url }
func markerForProvider(url string) string { return markerPrefix + "provider:" + url }

// IsMarker reports whether line is a ssh-gateway source annotation.
func IsMarker(line string) bool { return strings.HasPrefix(line, markerPrefix) }

// keySection groups key lines under a single source marker.
type keySection struct {
	marker string
	keys   []string
}

// parseExisting parses source-annotated authorized_keys lines into a map of
// marker → key lines. Returns nil when no markers are present, signalling that
// the file predates annotation (backward compat: treat as no existing state).
func parseExisting(lines []string) map[string][]string {
	for _, l := range lines {
		if IsMarker(l) {
			goto found
		}
	}
	return nil
found:
	sections := map[string][]string{}
	cur := ""
	for _, l := range lines {
		if IsMarker(l) {
			cur = l
			if _, ok := sections[cur]; !ok {
				sections[cur] = nil
			}
		} else if cur != "" && !strings.HasPrefix(l, "#") {
			sections[cur] = append(sections[cur], l)
		}
	}
	return sections
}

// resolveSourceKeys returns preserved keys from parsed[marker] when available,
// otherwise calls fetch. A nil parsed map (no annotations) always fetches.
func resolveSourceKeys(marker string, parsed map[string][]string, fetch func() ([]string, error)) ([]string, error) {
	if parsed != nil {
		if keys, ok := parsed[marker]; ok {
			return keys, nil
		}
		// marker absent but file had annotations → new source → fetch
	}
	return fetch()
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
		kt, ok := keyType(k)
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

// ResolveKeys resolves the authorized key lines for every configured user.
//
// When fetch is true (startup, periodic reconcile, or fetch_keys_on_reload),
// all keys are fetched from their sources. When fetch is false (config reload
// without fetch_keys_on_reload), existing is consulted: URL and provider
// sections whose source marker is present in the existing annotated file are
// preserved as-is; inline keys are always taken from the current config;
// sources absent from the current config are dropped; sources new to the
// config (marker not found in existing) are fetched. If existing[user] has no
// source markers (pre-annotation file), the full fetch path is used.
func (c *Config) ResolveKeys(fetch bool, existing map[string][]string) (map[string][]string, error) {
	provider := c.ProviderURL()
	m := make(map[string][]string, len(c.Users))

	for _, u := range c.Users {
		sections, err := c.resolveUserSections(u, provider, fetch, existing[u.Name])
		if err != nil {
			return nil, err
		}
		m[u.Name] = c.buildAnnotatedLines(sections, u.Name)
	}
	return m, nil
}

func (c *Config) resolveUserSections(u User, provider string, fetch bool, existingLines []string) ([]keySection, error) {
	var parsed map[string][]string
	if !fetch {
		parsed = parseExisting(existingLines)
	}

	if len(u.Keys) == 0 {
		providerURL := provider + "/" + u.Name + ".keys"
		marker := markerForProvider(providerURL)
		keys, err := resolveSourceKeys(marker, parsed, func() ([]string, error) {
			return keyfetch.Fetch(providerURL)
		})
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", u.Name, err)
		}
		return []keySection{{marker: marker, keys: keys}}, nil
	}

	var sections []keySection
	var inlineKeys []string
	for _, k := range u.Keys {
		k = strings.TrimSpace(k)
		if keyfetch.IsURL(k) {
			marker := markerForURL(k)
			url := k
			keys, err := resolveSourceKeys(marker, parsed, func() ([]string, error) {
				return keyfetch.Fetch(url)
			})
			if err != nil {
				return nil, fmt.Errorf("user %q: %w", u.Name, err)
			}
			sections = append(sections, keySection{marker: marker, keys: keys})
		} else {
			inlineKeys = append(inlineKeys, k)
		}
	}
	if len(inlineKeys) > 0 {
		sections = append([]keySection{{marker: markerInline, keys: inlineKeys}}, sections...)
	}
	return sections, nil
}

// buildAnnotatedLines applies SSH key validation, key_types filtering, and
// global dedup across sections, then returns the flat annotated lines
// (marker comment followed by key lines) ready for authorized_keys.
func (c *Config) buildAnnotatedLines(sections []keySection, user string) []string {
	seen := make(map[string]bool)
	var result []string
	totalKeys := 0
	for _, sec := range sections {
		filtered := sshKeys(sec.keys, user)
		filtered = c.filterKeys(filtered)
		var deduped []string
		for _, k := range filtered {
			if !seen[k] {
				seen[k] = true
				deduped = append(deduped, k)
			}
		}
		totalKeys += len(deduped)
		result = append(result, sec.marker)
		result = append(result, deduped...)
	}
	if totalKeys == 0 && len(sections) > 0 {
		slog.Warn("all keys filtered by key_types", "user", user)
	}
	return result
}
