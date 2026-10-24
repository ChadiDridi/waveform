package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPlanJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"plan", "--graph", "../../testdata/cases/chain/graph.json", "--change", "api,db", "--format", "json"}, &out, &errOut)
	if err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	var rep struct {
		Plan struct {
			Waves []struct{ Steps []struct{ Service string } }
		}
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Plan.Waves) != 2 || rep.Plan.Waves[0].Steps[0].Service != "db" {
		t.Fatalf("unexpected plan: %s", out.String())
	}
}

func TestGraphFromTraces(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"graph", "--traces", "../../testdata/cases/otlp/traces.json"}, &out, &errOut); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `"to": "stripe"`) {
		t.Fatalf("missing peer.service edge:\n%s", out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nope"},
		{"plan"},
		{"plan", "--graph", "x", "--traces", "y"},
		{"plan", "--graph", "../../testdata/cases/chain/graph.json", "--format", "yaml"},
		{"plan", "--graph", "../../testdata/cases/chain/graph.json", "--change", "api=rename"},
	} {
		var out, errOut bytes.Buffer
		if err := run(args, &out, &errOut); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want usage error", args, err)
		}
	}
}
