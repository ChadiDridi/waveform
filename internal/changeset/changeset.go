// Package changeset describes which services a change touches and how.
package changeset

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/ChadiDridi/waveform/internal/config"
)

// Kind is the shape of a change, which decides ordering against dependents.
type Kind string

const (
	// Additive changes (new endpoints, new fields) roll out providers first.
	Additive Kind = "additive"
	// Removal changes (dropping endpoints, fields) roll out consumers first.
	Removal Kind = "removal"
)

// Change is one changed service.
type Change struct {
	Service string   `json:"service"`
	Kind    Kind     `json:"kind"`
	Files   []string `json:"files,omitempty"`
}

// Parse parses "svc" (additive) or "svc=additive|removal".
func Parse(s string) (Change, error) {
	name, kind, found := strings.Cut(strings.TrimSpace(s), "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return Change{}, fmt.Errorf("empty service in change %q", s)
	}
	c := Change{Service: name, Kind: Additive}
	if found {
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "additive", "add":
		case "removal", "remove":
			c.Kind = Removal
		default:
			return Change{}, fmt.Errorf("change %q: kind must be additive or removal", s)
		}
	}
	return c, nil
}

// FromFiles maps changed files to services via cfg. All mapped changes are
// additive; override with explicit changes. Files owned by no service are
// returned as unmapped.
func FromFiles(cfg *config.Config, files []string) (changes []Change, unmapped []string) {
	by := map[string][]string{}
	for _, f := range files {
		svcs := cfg.ServicesFor(f)
		if len(svcs) == 0 {
			unmapped = append(unmapped, f)
		}
		for _, s := range svcs {
			by[s] = append(by[s], f)
		}
	}
	for s, fs := range by {
		changes = append(changes, Change{Service: s, Kind: Additive, Files: fs})
	}
	return Merge(changes), unmapped
}

// Merge combines changes per service. Removal wins over additive; files are
// unioned. Result is sorted by service.
func Merge(lists ...[]Change) []Change {
	by := map[string]*Change{}
	for _, l := range lists {
		for _, c := range l {
			m, ok := by[c.Service]
			if !ok {
				cc := c
				cc.Files = append([]string(nil), c.Files...)
				by[c.Service] = &cc
				continue
			}
			if c.Kind == Removal {
				m.Kind = Removal
			}
			m.Files = append(m.Files, c.Files...)
		}
	}
	out := make([]Change, 0, len(by))
	for _, c := range by {
		sort.Strings(c.Files)
		c.Files = dedupe(c.Files)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// GitDiff lists files changed in rev (anything `git diff --name-only`
// accepts, e.g. "origin/main...HEAD").
func GitDiff(rev string) ([]string, error) {
	out, err := exec.Command("git", "diff", "--name-only", rev).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git diff %s: %s", rev, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git diff %s: %w", rev, err)
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}
