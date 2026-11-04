package db

import (
	json "encoding/json/v2"
	"fmt"
)

// Hidden combos — port of AxonRouter's hiddenCombosRepo
// (src/lib/db/repos/hiddenCombosRepo.js). The storage layout is identical on
// purpose: a JSON array of combo names in the kv table under scope
// "hiddenCombos", key "names", so the state means the same thing in both
// dashboards. Separate from disabled models: that scope hides models per
// provider, this one hides a whole combo by name.

const (
	hiddenCombosScope = "hiddenCombos"
	hiddenCombosKey   = "names"
)

// GetHiddenComboNames returns the combo names hidden from the model catalog,
// in the order they were hidden. An unreadable stored value reads as an empty
// list, matching Axon's parseJson fallback.
func (r *Repo) GetHiddenComboNames() ([]string, error) {
	scope, err := r.GetKVScope(hiddenCombosScope)
	if err != nil {
		return nil, err
	}
	raw := scope[hiddenCombosKey]
	if raw == "" {
		return []string{}, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		return []string{}, nil
	}
	return names, nil
}

// HideCombos adds names to the hidden list. Idempotent: a name already hidden
// stays hidden once, in its original position.
func (r *Repo) HideCombos(names []string) error {
	if len(names) == 0 {
		return nil
	}
	current, err := r.GetHiddenComboNames()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(current)+len(names))
	merged := make([]string, 0, len(current)+len(names))
	for _, name := range current {
		if !seen[name] {
			seen[name] = true
			merged = append(merged, name)
		}
	}
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			merged = append(merged, name)
		}
	}
	return r.setHiddenComboNames(merged)
}

// UnhideCombos removes names from the hidden list. An empty names slice
// unhides everything (the kv row is deleted, as in Axon).
func (r *Repo) UnhideCombos(names []string) error {
	if len(names) == 0 {
		return r.DeleteKV(hiddenCombosScope, hiddenCombosKey)
	}
	current, err := r.GetHiddenComboNames()
	if err != nil {
		return err
	}
	remove := make(map[string]bool, len(names))
	for _, name := range names {
		remove[name] = true
	}
	next := make([]string, 0, len(current))
	for _, name := range current {
		if !remove[name] {
			next = append(next, name)
		}
	}
	if len(next) == 0 {
		return r.DeleteKV(hiddenCombosScope, hiddenCombosKey)
	}
	return r.setHiddenComboNames(next)
}

func (r *Repo) setHiddenComboNames(names []string) error {
	encoded, err := json.Marshal(names)
	if err != nil {
		return fmt.Errorf("encode hidden combos: %w", err)
	}
	return r.SetKV(hiddenCombosScope, hiddenCombosKey, string(encoded))
}
