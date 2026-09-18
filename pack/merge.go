package pack

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MergeRules controls semantic merging of settings documents.
type MergeRules struct {
	// UnionArrays lists dot-separated paths (e.g. "permissions.allow") whose
	// array values are concatenated and de-duplicated instead of overridden.
	// Every other array is replaced by the overlay's value.
	UnionArrays []string
}

func (r MergeRules) unionSet() map[string]bool {
	set := make(map[string]bool, len(r.UnionArrays))
	for _, p := range r.UnionArrays {
		set[p] = true
	}
	return set
}

// MergeJSON deep-merges overlay on top of base and returns the result.
//
//   - Object values merge key by key, recursively.
//   - Any other value: overlay wins.
//   - Arrays at paths listed in rules.UnionArrays: concatenated base-first,
//     duplicates removed, order preserved.
//
// Values must be generic JSON: map[string]any, []any, string, float64,
// bool or nil. A nil overlay value never replaces a base value; a nil
// base value is always replaced.
func MergeJSON(base, overlay any, rules MergeRules) any {
	return mergeValue(base, overlay, "", rules)
}

func mergeValue(base, overlay any, path string, rules MergeRules) any {
	if overlay == nil {
		return base
	}
	if base == nil {
		return overlay
	}
	bm, bok := base.(map[string]any)
	om, ook := overlay.(map[string]any)
	if bok && ook {
		out := make(map[string]any, len(bm)+len(om))
		for k, v := range bm {
			out[k] = v
		}
		for k, v := range om {
			if prev, ok := out[k]; ok {
				out[k] = mergeValue(prev, v, childPath(path, k), rules)
			} else {
				out[k] = v
			}
		}
		return out
	}
	if rules.unionSet()[path] {
		ba, baok := base.([]any)
		oa, oaok := overlay.([]any)
		if baok && oaok {
			return unionArrays(ba, oa)
		}
	}
	return overlay
}

func unionArrays(base, overlay []any) []any {
	out := make([]any, 0, len(base)+len(overlay))
	seen := make(map[string]bool, len(base)+len(overlay))
	for _, v := range base {
		k := canonKey(v)
		if !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	for _, v := range overlay {
		k := canonKey(v)
		if !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

func canonKey(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func childPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// MergeEnv overlays defaults onto base (a KEY=VALUE list, e.g. os.Environ)
// and returns the combined list.
//
// With force=false (org defaults) existing entries win and a note is
// recorded for every skipped key. With force=true (policy) defaults
// replace existing entries and a note is recorded for every override.
// Notes are deterministic: keys are processed in sorted order.
func MergeEnv(base []string, defaults map[string]string, force bool) (out, notes []string) {
	out = append([]string{}, base...)
	existing := make(map[string]int, len(base))
	for i, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if _, dup := existing[key]; !dup {
			existing[key] = i
		}
	}
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := defaults[k]
		if i, ok := existing[k]; ok {
			if force {
				out[i] = k + "=" + v
				notes = append(notes, fmt.Sprintf("env %s forced to %q (policy)", k, v))
			} else {
				notes = append(notes, fmt.Sprintf("env %s kept from environment (org default %q ignored)", k, v))
			}
			continue
		}
		out = append(out, k+"="+v)
		notes = append(notes, fmt.Sprintf("env %s=%q injected", k, v))
	}
	return out, notes
}
