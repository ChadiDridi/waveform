package config

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, file string
		want      bool
	}{
		{"services/api", "services/api/main.go", true},
		{"services/api", "services/api", true},
		{"services/api", "services/api-gw/main.go", false},
		{"services/api/", "services/api/x/y.go", true},
		{"services/**", "services/a/b/c.go", true},
		{"services/**/*.go", "services/main.go", true},
		{"services/**/*.go", "services/a/b/main.go", true},
		{"services/**/*.go", "services/a/b/README.md", false},
		{"*.proto", "api.proto", true},
		{"*.proto", "x/api.proto", false},
		{"**/*.proto", "x/api.proto", true},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.file); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pat, c.file, got, c.want)
		}
	}
}
