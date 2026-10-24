// Package config loads waveform.yaml: which repo paths belong to which
// service, and (optionally) each service's declared dependencies.
package config

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service is one deploy unit.
type Service struct {
	// Paths are repo-relative globs. "**" matches any number of path
	// segments; a pattern without glob characters matches as a directory
	// prefix.
	Paths []string `yaml:"paths"`
	// DependsOn lists declared providers. Omit to skip declared-vs-observed
	// checks for this service; use [] to declare "no dependencies".
	DependsOn []string `yaml:"dependsOn"`
}

// Config is the parsed waveform.yaml.
type Config struct {
	Services map[string]Service `yaml:"services"`
}

// Load reads and validates a config file.
func Load(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for name, s := range c.Services {
		for _, pat := range s.Paths {
			if _, err := path.Match(strings.ReplaceAll(pat, "**", "*"), ""); err != nil {
				return nil, fmt.Errorf("%s: service %s: bad path %q: %w", p, name, pat, err)
			}
		}
	}
	return &c, nil
}

// Declared returns declared dependencies keyed by consumer, only for services
// that set dependsOn.
func (c *Config) Declared() map[string][]string {
	if c == nil {
		return nil
	}
	out := map[string][]string{}
	for name, s := range c.Services {
		if s.DependsOn != nil {
			out[name] = s.DependsOn
		}
	}
	return out
}

// ServicesFor returns the services owning file (sorted). A file can belong to
// several services, e.g. a shared library path.
func (c *Config) ServicesFor(file string) []string {
	if c == nil {
		return nil
	}
	var out []string
	for name, s := range c.Services {
		for _, pat := range s.Paths {
			if Match(pat, file) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Match reports whether a slash-separated file path matches pattern.
func Match(pattern, file string) bool {
	pattern = strings.Trim(pattern, "/")
	file = strings.Trim(file, "/")
	if !strings.ContainsAny(pattern, "*?[") {
		return file == pattern || strings.HasPrefix(file, pattern+"/")
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
