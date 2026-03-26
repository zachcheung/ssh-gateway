package config

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v4"
)

type User struct {
	Name string   `yaml:"name"`
	Keys []string `yaml:"keys"`
}

type Config struct {
	Project string `yaml:"project"`
	Users   []User `yaml:"users"`
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

		if len(u.Keys) == 0 {
			return fmt.Errorf("user %q: at least one key is required", u.Name)
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

func (c *Config) UserMap() map[string][]string {
	m := make(map[string][]string, len(c.Users))
	for _, u := range c.Users {
		m[u.Name] = u.Keys
	}
	return m
}
