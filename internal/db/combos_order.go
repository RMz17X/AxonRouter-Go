package db

import (
	json "encoding/json/v2"
	"fmt"

	"9router/proxy/internal/models"
)

// Combo order — the persisted half of AxonRouter's reorderCombos
// (src/lib/localDb/combos.ts). Axon stores a sortOrder column on each combo;
// 9router-go's combos table has no such column, so the order lives in the kv
// table (scope "comboOrder", key "ids") as the full ordered id list, and
// GetCombos applies it on read. Ids in the stored order come first; combos
// the order does not name (created after the last reorder) keep their
// natural createdAt order behind them — the same result Axon's
// orderedIds-then-leftovers merge produces.

const (
	comboOrderScope = "comboOrder"
	comboOrderKey   = "ids"
)

// GetComboOrderIDs returns the persisted combo order, or an empty slice when
// no reorder has ever been saved.
func (r *Repo) GetComboOrderIDs() ([]string, error) {
	scope, err := r.GetKVScope(comboOrderScope)
	if err != nil {
		return nil, err
	}
	raw := scope[comboOrderKey]
	if raw == "" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil || ids == nil {
		return []string{}, nil
	}
	return ids, nil
}

// SetComboOrderIDs persists the full ordered combo id list.
func (r *Repo) SetComboOrderIDs(ids []string) error {
	encoded, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("encode combo order: %w", err)
	}
	return r.SetKV(comboOrderScope, comboOrderKey, string(encoded))
}

// OrderCombosByIDs sorts combos by the given id order: named combos first,
// in the order named (unknown ids and repeats ignored), then the rest in
// their incoming order.
func OrderCombosByIDs(combos []*models.Combo, ids []string) []*models.Combo {
	if len(ids) == 0 || len(combos) == 0 {
		return combos
	}
	byID := make(map[string]*models.Combo, len(combos))
	for _, combo := range combos {
		if combo != nil {
			byID[combo.ID] = combo
		}
	}
	ordered := make([]*models.Combo, 0, len(combos))
	seen := make(map[string]bool, len(combos))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if combo, ok := byID[id]; ok {
			seen[id] = true
			ordered = append(ordered, combo)
		}
	}
	for _, combo := range combos {
		if combo == nil || seen[combo.ID] {
			continue
		}
		seen[combo.ID] = true
		ordered = append(ordered, combo)
	}
	return ordered
}

// ReorderCombos persists comboIds as the combo order and returns every combo
// in the new order, mirroring Axon's reorderCombos: posted ids that exist
// come first in posted order, unposted combos keep their current order
// behind them.
func (r *Repo) ReorderCombos(comboIDs []string) ([]*models.Combo, error) {
	combos, err := r.GetCombos()
	if err != nil {
		return nil, err
	}
	ordered := OrderCombosByIDs(combos, comboIDs)
	ids := make([]string, 0, len(ordered))
	for _, combo := range ordered {
		if combo != nil {
			ids = append(ids, combo.ID)
		}
	}
	if err := r.SetComboOrderIDs(ids); err != nil {
		return nil, err
	}
	return ordered, nil
}
