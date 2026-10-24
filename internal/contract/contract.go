// Package contract checks API version windows against a wave plan.
//
// A contract is one API: the provider lists the versions it serves before and
// after the change, each consumer the version it calls before and after.
// While a service's wave is in flight both its old and new pods run, so:
//
//   - an in-flight consumer may call with either version;
//   - an in-flight provider may answer from either pod set, so a caller's
//     version must be served by both.
//
// A plan is valid when, during every wave and after the last one, every
// version a consumer may send is served by every provider pod set it may hit.
package contract

import (
	"fmt"
	"sort"
	"strings"
)

// Contract is one API between a provider and its consumers.
type Contract struct {
	Provider  string         `yaml:"provider" json:"provider"`
	Serves    Window         `yaml:"serves" json:"serves"`
	Consumers map[string]Use `yaml:"consumers" json:"consumers"`
}

// Window is the set of versions served before and after the change.
// An empty After means unchanged.
type Window struct {
	Before []string `yaml:"before" json:"before"`
	After  []string `yaml:"after" json:"after,omitempty"`
}

// Use is the version a consumer calls before and after the change.
// An empty After means unchanged.
type Use struct {
	Before string `yaml:"before" json:"before"`
	After  string `yaml:"after" json:"after,omitempty"`
}

// Violation is one incompatible state.
type Violation struct {
	Contract string `json:"contract"`
	Consumer string `json:"consumer"`
	Provider string `json:"provider"`
	// Wave is when the break happens; len(waves)+1 means after the rollout.
	Wave    int    `json:"wave"`
	Message string `json:"message"`
}

// Check returns violations of contracts for waves, given as service names
// per wave in rollout order. Services in no wave keep their Before versions.
func Check(contracts map[string]Contract, waves [][]string) []Violation {
	waveOf := map[string]int{}
	for i, w := range waves {
		for _, s := range w {
			waveOf[s] = i + 1
		}
	}
	names := make([]string, 0, len(contracts))
	for n := range contracts {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []Violation
	for _, name := range names {
		c := contracts[name]
		serveAfter := c.Serves.After
		if len(serveAfter) == 0 {
			serveAfter = c.Serves.Before
		}
		consumers := make([]string, 0, len(c.Consumers))
		for n := range c.Consumers {
			consumers = append(consumers, n)
		}
		sort.Strings(consumers)
		for _, cons := range consumers {
			use := c.Consumers[cons]
			useAfter := use.After
			if useAfter == "" {
				useAfter = use.Before
			}
			for k := 1; k <= len(waves)+1; k++ {
				sends := pick(waveOf[cons], k, []string{use.Before}, []string{useAfter})
				pods := pickSets(waveOf[c.Provider], k, c.Serves.Before, serveAfter)
				if msg := incompatible(sends, pods); msg != "" {
					out = append(out, Violation{
						Contract: name, Consumer: cons, Provider: c.Provider, Wave: k,
						Message: fmt.Sprintf("%s: %s %s", name, cons, msg),
					})
					break // report the first broken wave per pair
				}
			}
		}
	}
	return out
}

// pick returns the versions a consumer may send during wave k.
func pick(svcWave, k int, before, after []string) []string {
	switch {
	case svcWave == 0 || svcWave > k:
		return before
	case svcWave < k:
		return after
	}
	return append(append([]string(nil), before...), after...)
}

// pickSets returns the serving sets a request may land on during wave k.
func pickSets(svcWave, k int, before, after []string) [][]string {
	switch {
	case svcWave == 0 || svcWave > k:
		return [][]string{before}
	case svcWave < k:
		return [][]string{after}
	}
	return [][]string{before, after}
}

func incompatible(sends []string, pods [][]string) string {
	for _, v := range sends {
		for _, set := range pods {
			if !contains(set, v) {
				return fmt.Sprintf("may call %s but a provider pod set serves only [%s]", v, strings.Join(set, ", "))
			}
		}
	}
	return ""
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
