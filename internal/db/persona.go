package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// PersonaDef is one Persona: a label, one preset from the jailbreak
// library, and the set of names that preset is BOUND to — model ids
// ("provider/model") and/or combo names. Whenever a bound name serves —
// requested directly, or as the leg a combo actually forwards to — the
// persona's preset is injected for that leg, ahead of the standing
// keyword rules. A bound name wears at most one persona; the dashboard
// handlers enforce that at write time.
//
// Personas used to be virtual models (a minted name routing to one
// target); that layer is retired — a persona name is no longer
// requestable and personas no longer appear in the model catalog. The
// Target field survives only as a read path for definitions stored in
// the old shape: BoundNames folds it into the binding set, and the
// next save rewrites the row in the new shape.
type PersonaDef struct {
	Name     string   `json:"name"`
	PresetID string   `json:"presetId"`
	Models   []string `json:"models,omitempty"`
	// Target is the retired virtual-model shape (one routed name).
	// Read-only legacy: never written by current code.
	Target string `json:"target,omitempty"`
}

// BoundNames returns the names this persona's preset is bound to: the
// stored binding set, or — for a definition still in the retired
// virtual-model shape — its single target.
func (p *PersonaDef) BoundNames() []string {
	if p == nil {
		return nil
	}
	if len(p.Models) > 0 {
		return p.Models
	}
	if p.Target != "" {
		return []string{p.Target}
	}
	return nil
}

// personaScope is the kv scope persona definitions live under, one row
// per persona, keyed by the persona's name.
const personaScope = "persona"

// personaSnapshot is the immutable in-memory copy of every persona
// definition, mirroring the jailbreak ruleset snapshot: the serving
// path resolves persona bindings per request and per attempt, so it
// reads these maps and never queries. Writes replace the whole
// snapshot. byBound maps a bound model/combo name to its persona; when
// two stored definitions claim one name (only possible for rows
// written before the one-persona rule), the alphabetically first
// persona wins so the outcome is deterministic.
type personaSnapshot struct {
	byName  map[string]*PersonaDef
	byBound map[string]*PersonaDef
}

// personaSnapshot returns the current snapshot, building it on first
// use or after a write invalidated it. A failed build yields an empty
// snapshot for that call rather than an error: personas are an
// optimisation of naming, and a store hiccup must not fail requests —
// the name simply resolves as an ordinary model or combo.
func (r *Repo) currentPersonaSnapshot() *personaSnapshot {
	if snap, ok := r.personaSnap.Load().(*personaSnapshot); ok && snap != nil {
		return snap
	}
	r.personaBuild.Lock()
	defer r.personaBuild.Unlock()
	if snap, ok := r.personaSnap.Load().(*personaSnapshot); ok && snap != nil {
		return snap
	}
	snap := &personaSnapshot{byName: map[string]*PersonaDef{}, byBound: map[string]*PersonaDef{}}
	if defs, err := r.loadPersonasFromStore(); err == nil {
		sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
		for _, p := range defs {
			snap.byName[p.Name] = p
			for _, bound := range p.BoundNames() {
				if _, claimed := snap.byBound[bound]; !claimed {
					snap.byBound[bound] = p
				}
			}
		}
	}
	r.personaSnap.Store(snap)
	return snap
}

// invalidatePersonaSnapshot drops the snapshot so the next read
// rebuilds it. Every persona write calls this.
func (r *Repo) invalidatePersonaSnapshot() {
	r.personaSnap.Store((*personaSnapshot)(nil))
}

// GetPersona returns the persona with this name, or nil when no
// persona carries it. Dashboard CRUD reads personas by their label
// through here; the serving path uses GetPersonaForBinding instead.
func (r *Repo) GetPersona(name string) (*PersonaDef, error) {
	if name == "" {
		return nil, nil
	}
	return r.currentPersonaSnapshot().byName[name], nil
}

// GetPersonaForBinding returns the persona whose preset is bound to
// this model id or combo name, or nil when the name is unbound — the
// ordinary case on the serving path.
func (r *Repo) GetPersonaForBinding(name string) (*PersonaDef, error) {
	if name == "" {
		return nil, nil
	}
	return r.currentPersonaSnapshot().byBound[name], nil
}

// ListPersonas returns every persona definition, ordered by name.
// Each entry is a copy normalized to the current shape — the binding
// set in Models, the legacy Target folded in and cleared — so API
// consumers never see the retired virtual-model shape.
func (r *Repo) ListPersonas() ([]*PersonaDef, error) {
	snap := r.currentPersonaSnapshot()
	out := make([]*PersonaDef, 0, len(snap.byName))
	for _, p := range snap.byName {
		cp := *p
		cp.Models = p.BoundNames()
		cp.Target = ""
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// loadPersonasFromStore reads every persona row from the kv store; it
// backs the snapshot build and nothing else.
func (r *Repo) loadPersonasFromStore() ([]*PersonaDef, error) {
	rows, err := r.QueryContext(context.Background(),
		`SELECT key, value FROM kv WHERE scope = ?`, personaScope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*PersonaDef{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var p PersonaDef
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("parse persona %s: %w", key, err)
		}
		p.Name = key
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SavePersona creates or replaces one persona definition, keyed by its
// name. Validation (name shape, preset existence, one persona per
// bound name) belongs to the dashboard handler; the store normalizes
// the shape: a legacy Target folds into the binding set, duplicates
// drop out, and the row is always written in the current shape.
func (r *Repo) SavePersona(p *PersonaDef) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("persona needs a name")
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(p.Models)+1)
	for _, bound := range p.BoundNames() {
		if bound == "" || seen[bound] {
			continue
		}
		seen[bound] = true
		models = append(models, bound)
	}
	p.Models = models
	p.Target = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		personaScope, p.Name, string(raw),
	)
	if err == nil {
		r.invalidatePersonaSnapshot()
	}
	return err
}

// DeletePersona removes one persona definition. Deleting a name that
// does not exist is a no-op.
func (r *Repo) DeletePersona(name string) error {
	if name == "" {
		return nil
	}
	_, err := r.ExecContext(context.Background(),
		`DELETE FROM kv WHERE scope = ? AND key = ?`, personaScope, name)
	if err == nil {
		r.invalidatePersonaSnapshot()
	}
	return err
}
