package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpsert(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing []comment
		want     string
	}{
		{"create", []comment{{ID: 1, Body: "lgtm"}}, "POST /repos/o/r/issues/7/comments"},
		{"update", []comment{{ID: 1, Body: "lgtm"}, {ID: 42, Body: "<!-- m --> old"}}, "PATCH /repos/o/r/issues/comments/42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("missing auth header")
				}
				if r.Method == http.MethodGet {
					json.NewEncoder(w).Encode(tc.existing)
					return
				}
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["body"] != "<!-- m --> new" {
					t.Errorf("body = %q", body["body"])
				}
				writes = append(writes, r.Method+" "+r.URL.Path)
				w.Write([]byte("{}"))
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Token: "tok"}
			if err := c.Upsert(context.Background(), "o/r", 7, "<!-- m -->", "<!-- m --> new"); err != nil {
				t.Fatal(err)
			}
			if len(writes) != 1 || writes[0] != tc.want {
				t.Fatalf("writes = %v, want [%s]", writes, tc.want)
			}
		})
	}
}

func TestUpsertError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Token: "tok"}
	if err := c.Upsert(context.Background(), "o/r", 1, "m", "m"); err == nil {
		t.Fatal("want error")
	}
}
