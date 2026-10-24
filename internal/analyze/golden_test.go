package analyze

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/config"
	"github.com/ChadiDridi/waveform/internal/graph"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Each directory under testdata/cases holds:
//
//	graph.json | traces.json   observed graph or OTLP/JSON spans
//	changes.txt                optional, one "svc[=removal]" per line
//	waveform.yaml, files.txt   optional, changed files mapped via config
//	want.json, want.md         golden output
func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob("../../testdata/cases/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no golden cases")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			rep, _ := Run(loadCase(t, dir))
			js, err := rep.JSON()
			if err != nil {
				t.Fatal(err)
			}
			compare(t, filepath.Join(dir, "want.json"), js)
			compare(t, filepath.Join(dir, "want.md"), []byte(rep.Markdown()))
		})
	}
}

func loadCase(t *testing.T, dir string) Input {
	t.Helper()
	var in Input
	var err error
	if exists(filepath.Join(dir, "graph.json")) {
		in.Graph, err = graph.Load(filepath.Join(dir, "graph.json"))
	} else {
		b := graph.NewBuilder()
		err = b.AddFile(filepath.Join(dir, "traces.json"))
		in.Graph = b.Graph(0)
	}
	if err != nil {
		t.Fatal(err)
	}

	var mapped, explicit []changeset.Change
	if p := filepath.Join(dir, "waveform.yaml"); exists(p) {
		cfg, err := config.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		in.Declared = cfg.Declared()
		in.Irreversible = cfg.Irreversible()
		in.Contracts = cfg.Contracts
		in.MinExposure = cfg.Gate.MinExposure
		mapped, in.Unmapped = changeset.FromFiles(cfg, lines(t, filepath.Join(dir, "files.txt")))
	}
	for _, l := range lines(t, filepath.Join(dir, "changes.txt")) {
		c, err := changeset.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		explicit = append(explicit, c)
	}
	in.Changes = changeset.Merge(mapped, explicit)
	return in
}

func lines(t *testing.T, p string) []string {
	t.Helper()
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func compare(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./... -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s mismatch (run go test ./... -update and review the diff)\n--- got ---\n%s", path, got)
	}
}
