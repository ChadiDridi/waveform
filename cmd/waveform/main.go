// Command waveform plans multi-service deploys from the observed service graph.
//
//	waveform graph --traces traces.json [--window 300] [-o graph.json]
//	waveform plan  (--graph graph.json | --traces traces.json) [--config waveform.yaml]
//	               [--change svc[=removal]]... [--git-diff origin/main...HEAD]
//	               [--format markdown|json] [--github-comment [--pr N]]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ChadiDridi/waveform/internal/analyze"
	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/config"
	"github.com/ChadiDridi/waveform/internal/github"
	"github.com/ChadiDridi/waveform/internal/graph"
)

var version = "dev"

const usage = `waveform plans multi-service deploys from the observed service graph.

Usage:
  waveform graph  build a service graph from OTLP/JSON traces
  waveform plan   compute blast radius and deploy waves for a change set
  waveform version

Run "waveform <command> -h" for flags.
`

// errUsage marks errors that should exit 2.
var errUsage = errors.New("usage")

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, "waveform:", err)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "waveform:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("%w: missing command", errUsage)
	}
	switch args[0] {
	case "graph":
		return runGraph(args[1:], stdout, stderr)
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

type graphFlags struct {
	graphFile string
	traces    listFlag
	window    float64
}

func (g *graphFlags) register(fs *flag.FlagSet, allowGraph bool) {
	if allowGraph {
		fs.StringVar(&g.graphFile, "graph", "", "observed graph JSON (from `waveform graph`)")
	}
	fs.Var(&g.traces, "traces", "OTLP/JSON trace file(s), comma-separated or repeated")
	fs.Float64Var(&g.window, "window", 0, "observation window in seconds for RPS (default: span time range)")
}

func (g *graphFlags) load() (*graph.Graph, error) {
	switch {
	case g.graphFile != "" && len(g.traces) > 0:
		return nil, fmt.Errorf("%w: use --graph or --traces, not both", errUsage)
	case g.graphFile != "":
		return graph.Load(g.graphFile)
	case len(g.traces) > 0:
		b := graph.NewBuilder()
		for _, f := range g.traces {
			if err := b.AddFile(f); err != nil {
				return nil, err
			}
		}
		return b.Graph(g.window), nil
	}
	return nil, fmt.Errorf("%w: need --graph or --traces", errUsage)
}

func runGraph(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var gf graphFlags
	gf.register(fs, false)
	out := fs.String("o", "", "write graph JSON here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := gf.load()
	if err != nil {
		return err
	}
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	return writeJSON(w, g)
}

func runPlan(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var gf graphFlags
	gf.register(fs, true)
	var changes listFlag
	fs.Var(&changes, "change", "changed service, `svc[=additive|removal]`; repeatable")
	cfgPath := fs.String("config", "", "waveform.yaml mapping paths to services (default: ./waveform.yaml if present)")
	gitDiff := fs.String("git-diff", "", "derive changes from `git diff --name-only <rev>`, e.g. origin/main...HEAD")
	format := fs.String("format", "markdown", "output format: markdown or json")
	comment := fs.Bool("github-comment", false, "post or update the plan as a PR comment (needs GITHUB_TOKEN, GITHUB_REPOSITORY)")
	pr := fs.Int("pr", 0, "pull request number for --github-comment (default: from GITHUB_EVENT_PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "markdown" && *format != "json" {
		return fmt.Errorf("%w: --format must be markdown or json", errUsage)
	}

	g, err := gf.load()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}

	var explicit, mapped []changeset.Change
	var unmapped []string
	for _, c := range changes {
		ch, err := changeset.Parse(c)
		if err != nil {
			return fmt.Errorf("%w: %v", errUsage, err)
		}
		explicit = append(explicit, ch)
	}
	if *gitDiff != "" {
		if cfg == nil {
			return fmt.Errorf("%w: --git-diff needs --config (or ./waveform.yaml) to map files to services", errUsage)
		}
		files, err := changeset.GitDiff(*gitDiff)
		if err != nil {
			return err
		}
		mapped, unmapped = changeset.FromFiles(cfg, files)
	}

	rep, planErr := analyze.Run(analyze.Input{
		Graph:    g,
		Changes:  changeset.Merge(mapped, explicit),
		Declared: cfg.Declared(),
		Unmapped: unmapped,
	})

	if *format == "json" {
		b, err := rep.JSON()
		if err != nil {
			return err
		}
		if _, err := stdout.Write(b); err != nil {
			return err
		}
	} else {
		fmt.Fprint(stdout, rep.Markdown())
	}

	if *comment {
		if err := postComment(*pr, rep.Markdown()); err != nil {
			return fmt.Errorf("github comment: %w", err)
		}
	}
	return planErr
}

func loadConfig(p string) (*config.Config, error) {
	if p == "" {
		if _, err := os.Stat("waveform.yaml"); err != nil {
			return nil, nil
		}
		p = "waveform.yaml"
	}
	return config.Load(p)
}

func postComment(pr int, body string) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return fmt.Errorf("GITHUB_REPOSITORY is not set")
	}
	if pr == 0 {
		n, err := github.PRFromEvent()
		if err != nil {
			return err
		}
		pr = n
	}
	c, err := github.FromEnv()
	if err != nil {
		return err
	}
	return c.Upsert(context.Background(), repo, pr, analyze.Marker, body)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
