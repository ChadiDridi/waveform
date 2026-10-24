package changeset

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Change{
		"api":          {Service: "api", Kind: Additive},
		" api = add ":  {Service: "api", Kind: Additive},
		"api=removal":  {Service: "api", Kind: Removal},
		"api=REMOVE":   {Service: "api", Kind: Removal},
		"api=additive": {Service: "api", Kind: Additive},
	} {
		got, err := Parse(in)
		if err != nil || got.Service != want.Service || got.Kind != want.Kind {
			t.Errorf("Parse(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "=removal", "api=rename"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q): want error", bad)
		}
	}
}

func TestMergeRemovalWins(t *testing.T) {
	got := Merge(
		[]Change{{Service: "b", Kind: Additive, Files: []string{"y", "x"}}, {Service: "a", Kind: Additive}},
		[]Change{{Service: "b", Kind: Removal, Files: []string{"x"}}},
	)
	want := []Change{
		{Service: "a", Kind: Additive},
		{Service: "b", Kind: Removal, Files: []string{"x", "y"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %+v", got)
	}
}
