package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/ChadiDridi/waveform/internal/argo"
	"github.com/ChadiDridi/waveform/internal/collect"
	"github.com/ChadiDridi/waveform/internal/controller"
	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/gate"
	"github.com/ChadiDridi/waveform/internal/kube"
	"github.com/ChadiDridi/waveform/internal/prom"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// newExecutor wires the Argo Rollouts actuator and, when configured, the
// Prometheus gate. WAVEFORM_PROMETHEUS_TOKEN is sent as a bearer token.
func newExecutor(dc dynamic.Interface, namespace string, spec *waveplan.Spec, poll time.Duration) (*execute.Executor, error) {
	ex := &execute.Executor{Act: &argo.Actuator{Client: dc, Namespace: namespace}, Poll: poll}
	if spec.Gate.PrometheusURL != "" {
		pc := prom.New(spec.Gate.PrometheusURL)
		if tok := os.Getenv("WAVEFORM_PROMETHEUS_TOKEN"); tok != "" {
			pc.Headers = map[string]string{"Authorization": "Bearer " + tok}
		}
		g, err := gate.NewPrometheus(pc, spec.Gate)
		if err != nil {
			return nil, err
		}
		ex.Gate = g
	}
	return ex, nil
}

func runRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "", "WavePlan YAML (from `waveform plan --format waveplan`)")
	statePath := fs.String("state", "", "status file, used to resume (default: <file>.status.json)")
	var approve listFlag
	fs.Var(&approve, "approve", "approve an irreversible step, `service`; repeatable")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: $KUBECONFIG, ~/.kube/config, in-cluster)")
	kctx := fs.String("context", "", "kubeconfig context")
	poll := fs.Duration("poll", 5*time.Second, "how often to observe rollouts")
	once := fs.Bool("once", false, "advance one step, save state and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("%w: -f is required", errUsage)
	}
	if *statePath == "" {
		*statePath = strings.TrimSuffix(strings.TrimSuffix(*file, ".yaml"), ".yml") + ".status.json"
	}
	wp, err := waveplan.Load(*file)
	if err != nil {
		return err
	}
	spec := wp.Spec
	spec.Approvals = append(spec.Approvals, approve...)
	spec.Defaults()
	if err := spec.Validate(); err != nil {
		return err
	}
	st, err := waveplan.LoadStatus(*statePath)
	if err != nil {
		return fmt.Errorf("%s: %w", *statePath, err)
	}
	dc, ns, err := kube.Dynamic(*kubeconfig, *kctx)
	if err != nil {
		return err
	}
	if wp.Metadata.Namespace != "" {
		ns = wp.Metadata.Namespace
	}
	ex, err := newExecutor(dc, ns, &spec, *poll)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	last := ""
	for !st.Terminal() {
		d, err := ex.Advance(ctx, &spec, &st)
		if err != nil {
			return err
		}
		if err := waveplan.SaveStatus(*statePath, st); err != nil {
			return err
		}
		line := fmt.Sprintf("%-11s wave %d/%d  %s", st.Phase, st.Wave, len(spec.Waves), st.Message)
		if line != last {
			fmt.Fprintf(stdout, "%s  %s\n", time.Now().Format("15:04:05"), line)
			last = line
		}
		if *once || st.Terminal() {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted; state saved to %s, rerun the same command to resume", *statePath)
		case <-time.After(d):
		}
	}
	switch st.Phase {
	case waveplan.PhaseRolledBack:
		return errors.New(st.Message)
	case waveplan.PhaseHalted:
		return fmt.Errorf("halted, needs a human: %s", st.Message)
	}
	return nil
}

func runController(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: $KUBECONFIG, ~/.kube/config, in-cluster)")
	kctx := fs.String("context", "", "kubeconfig context")
	namespace := fs.String("namespace", "", "only reconcile WavePlans in this namespace (default: all)")
	tick := fs.Duration("tick", 5*time.Second, "reconcile interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dc, _, err := kube.Dynamic(*kubeconfig, *kctx)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	c := &controller.Controller{
		Client:    dc,
		Namespace: *namespace,
		Tick:      *tick,
		Log:       log,
		Executor: func(ns string, spec *waveplan.Spec) (*execute.Executor, error) {
			return newExecutor(dc, ns, spec, *tick)
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("controller started", "namespace", *namespace, "version", version)
	return c.Run(ctx)
}

func runCollect(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":4318", "OTLP/HTTP listen address")
	out := fs.String("o", "", "graph JSON file to keep up to date (also served at /graph)")
	flush := fs.Duration("flush", 30*time.Second, "how often to write -o")
	grace := fs.Duration("grace", 2*time.Minute, "how long a client span waits for its server span")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	c := collect.New(*grace, log)
	srv := &http.Server{Addr: *listen, Handler: c.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("collecting", "listen", *listen, "out", *out)

	t := time.NewTicker(*flush)
	defer t.Stop()
	for {
		select {
		case err := <-errc:
			return err
		case <-t.C:
			if *out != "" {
				if err := c.WriteFile(*out); err != nil {
					log.Error("write graph", "err", err)
				}
			}
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(shutCtx)
			if *out != "" {
				return c.WriteFile(*out)
			}
			return nil
		}
	}
}
