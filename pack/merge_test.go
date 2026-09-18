package pack

import (
	"reflect"
	"testing"
)

func TestMergeJSONUserWinsScalars(t *testing.T) {
	base := map[string]any{
		"model": "sonnet",
		"env":   map[string]any{"A": "1", "B": "2"},
	}
	overlay := map[string]any{
		"model": "opus",
		"env":   map[string]any{"B": "3", "C": "4"},
	}
	got := MergeJSON(base, overlay, MergeRules{})
	want := map[string]any{
		"model": "opus",
		"env":   map[string]any{"A": "1", "B": "3", "C": "4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMergeJSONUnionArrays(t *testing.T) {
	base := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"a", "b", "c"},
		},
	}
	overlay := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"b", "d"},
		},
	}
	rules := MergeRules{UnionArrays: []string{"permissions.allow"}}
	got := MergeJSON(base, overlay, rules)
	want := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"a", "b", "c", "d"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMergeJSONArraysOverrideOutsideUnionPaths(t *testing.T) {
	base := map[string]any{"tags": []any{"a", "b"}}
	overlay := map[string]any{"tags": []any{"c"}}
	got := MergeJSON(base, overlay, MergeRules{UnionArrays: []string{"other"}})
	want := map[string]any{"tags": []any{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMergeJSONNilHandling(t *testing.T) {
	base := map[string]any{"a": "keep", "b": "gone"}
	overlay := map[string]any{"b": nil, "c": "new"}
	got := MergeJSON(base, overlay, MergeRules{})
	want := map[string]any{"a": "keep", "b": "gone", "c": "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if MergeJSON(nil, "x", MergeRules{}) != "x" {
		t.Fatal("nil base should take overlay value")
	}
	if MergeJSON("y", nil, MergeRules{}) != "y" {
		t.Fatal("nil overlay should keep base value")
	}
}

func TestMergeJSONNestedUnion(t *testing.T) {
	base := map[string]any{"p": map[string]any{"q": map[string]any{"allow": []any{"1"}}}}
	overlay := map[string]any{"p": map[string]any{"q": map[string]any{"allow": []any{"2"}}}}
	rules := MergeRules{UnionArrays: []string{"p.q.allow"}}
	got := MergeJSON(base, overlay, rules)
	want := map[string]any{"p": map[string]any{"q": map[string]any{"allow": []any{"1", "2"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMergeEnvDefaults(t *testing.T) {
	base := []string{"PATH=/usr/bin", "DISABLE_TELEMETRY=custom"}
	defaults := map[string]string{"DISABLE_TELEMETRY": "1", "NEW_KEY": "x"}
	out, notes := MergeEnv(base, defaults, false)
	want := []string{"PATH=/usr/bin", "DISABLE_TELEMETRY=custom", "NEW_KEY=x"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %v", notes)
	}
}

func TestMergeEnvForced(t *testing.T) {
	base := []string{"DISABLE_TELEMETRY=custom"}
	defaults := map[string]string{"DISABLE_TELEMETRY": "1"}
	out, notes := MergeEnv(base, defaults, true)
	want := []string{"DISABLE_TELEMETRY=1"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %v", notes)
	}
}
