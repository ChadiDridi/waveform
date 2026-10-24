// Package github posts or updates waveform's pull request comment.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Client talks to the GitHub REST API.
type Client struct {
	HTTP    *http.Client
	BaseURL string // e.g. https://api.github.com
	Token   string
}

// FromEnv builds a client from GITHUB_TOKEN and GITHUB_API_URL.
func FromEnv() (*Client, error) {
	tok := os.Getenv("GITHUB_TOKEN")
	if tok == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN is not set")
	}
	base := os.Getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Client{HTTP: http.DefaultClient, BaseURL: strings.TrimRight(base, "/"), Token: tok}, nil
}

type comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// Upsert edits the first comment on the PR containing marker, or creates one.
// repo is "owner/name".
func (c *Client) Upsert(ctx context.Context, repo string, pr int, marker, body string) error {
	for page := 1; ; page++ {
		var cs []comment
		url := fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100&page=%d", c.BaseURL, repo, pr, page)
		if err := c.do(ctx, http.MethodGet, url, nil, &cs); err != nil {
			return err
		}
		for _, cm := range cs {
			if strings.Contains(cm.Body, marker) {
				url := fmt.Sprintf("%s/repos/%s/issues/comments/%d", c.BaseURL, repo, cm.ID)
				return c.do(ctx, http.MethodPatch, url, map[string]string{"body": body}, nil)
			}
		}
		if len(cs) < 100 {
			break
		}
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.BaseURL, repo, pr)
	return c.do(ctx, http.MethodPost, url, map[string]string{"body": body}, nil)
}

func (c *Client) do(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// PRFromEvent reads the pull request number from the GITHUB_EVENT_PATH payload.
func PRFromEvent() (int, error) {
	p := os.Getenv("GITHUB_EVENT_PATH")
	if p == "" {
		return 0, fmt.Errorf("GITHUB_EVENT_PATH is not set; pass --pr")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	var ev struct {
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return 0, fmt.Errorf("parse %s: %w", p, err)
	}
	if n := ev.PullRequest.Number; n > 0 {
		return n, nil
	}
	if ev.Number > 0 {
		return ev.Number, nil
	}
	return 0, fmt.Errorf("no pull request number in %s; pass --pr", p)
}
