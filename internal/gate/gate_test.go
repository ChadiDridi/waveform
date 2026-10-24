package gate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChadiDridi/waveform/internal/prom"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

func TestCheck(t *testing.T) {
	values := map[string]string{"api": "3.5", "web": "0.2", "nan": "NaN"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		for svc, v := range values {
			if strings.Contains(q, `svc="`+svc+`"`) {
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"%s"]}]}}`, v)
				return
			}
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	spec := waveplan.GateSpec{Query: `burn{svc="{{.Service}}"}[{{.Window}}] / (1-{{.Objective}})`, Window: waveplan.Duration(90 * time.Second)}
	spec.Defaults()
	g, err := NewPrometheus(prom.New(srv.URL), spec)
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := g.Query("api"); q != `burn{svc="api"}[90s] / (1-0.999)` {
		t.Fatalf("query = %s", q)
	}
	got, err := g.Check(context.Background(), []string{"api", "web", "nan", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	want := []waveplan.Reading{
		{Service: "api", BurnRate: 3.5, Breached: true},
		{Service: "web", BurnRate: 0.2},
		{Service: "nan", NoData: true},
		{Service: "missing", NoData: true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}

	g.Spec.OnNoData = "fail"
	got, _ = g.Check(context.Background(), []string{"missing"})
	if !got[0].Breached {
		t.Fatal("onNoData=fail should breach")
	}
}

func TestPromDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Minute: "5m", 2 * time.Hour: "2h", 90 * time.Second: "90s", 0: "1s",
	} {
		if got := promDuration(d); got != want {
			t.Errorf("promDuration(%v) = %s, want %s", d, got, want)
		}
	}
}
