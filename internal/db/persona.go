package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// PersonaDef is one Persona virtual model: a client-facing name that is
// NOT a real model, a combo, or an alias, routing to exactly one target
// — a model id ("provider/model") or a combo name — and always carrying
// one preset from the jailbreak library, whose document supplies the
// prompt (or chat turns), position, and safety flag at injection time.
//
// Personas replace the retired per-model binding layer: instead of
// arming prompts on names that already exist, an operator mints a new
// name whose whole identity is "this target, wearing this prompt".
// Names are unique across the model catalog, the combo names, the
// model aliases, and the other personas; the dashboard handlers enforce
// that at write time, and the serving path resolves a persona before
// combos so a stored name can never be shadowed.
type PersonaDef struct {
	Name     string `json:"name"`
	Target   string `json:"target"`
	PresetID string `json:"presetId"`
}

// personaScope is the kv scope persona definitions live under, one row
// per persona, keyed by the persona's name.
const personaScope = "persona"

// GetPersona loads one persona by name. A missing name returns
// (nil, nil): "not a persona" is the ordinary case on the serving
// path, not an error.
func (r *Repo) GetPersona(name string) (*PersonaDef, error) {
	if name == "" {
		return nil, nil
	}
	var raw string
	err := r.QueryRowContext(context.Background(),
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		personaScope, name,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PersonaDef
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("parse persona %s: %w", name, err)
	}
	p.Name = name
	return &p, nil
}

// ListPersonas returns every persona definition, ordered by name.
func (r *Repo) ListPersonas() ([]*PersonaDef, error) {
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SavePersona creates or replaces one persona definition, keyed by its
// name. Validation (name shape, target and preset existence, collision
// freedom) belongs to the dashboard handler; the store keeps what it
// is given.
func (r *Repo) SavePersona(p *PersonaDef) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("persona needs a name")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		personaScope, p.Name, string(raw),
	)
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
	return err
}
