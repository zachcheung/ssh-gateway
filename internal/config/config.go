package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/zachcheung/ssh-gateway/internal/keyfetch"
	"go.yaml.in/yaml/v4"
)

var providerShorthands = map[string]string{
	"github": "https://github.com",
	"gitlab": "https://gitlab.com",
}

type User struct {
	Name string   `yaml:"name"`
	Keys []string `yaml:"keys"`
}

type Config struct {
	Project     string `yaml:"project"`
	KeyProvider string `yaml:"key_provider"`
	Users       []User `yaml:"users"`
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

		m[u.Name] = keys
	}

	return m, nil
}
