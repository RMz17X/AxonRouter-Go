package db

import (
	"fmt"
)

// Full-column connection writes for the credentials importer (batch 7,
// AxonRouter parity). The dashboard's CreateProviderConnectionFull /
// UpdateProviderConnection cannot express two things the importer needs:
// the email column, and an explicit isActive on create. These two methods
// mirror their sealing behaviour exactly (insert/update first, then
// sealOnWrite, then store the sealed payload when it differs).

// CreateImportedConnection inserts a provider connection with every column
// the credential backup format carries.
func (r *Repo) CreateImportedConnection(id, provider, authType, name string, email *string, priority *int, isActive bool, dataJSON string) error {
	if dataJSON == "" {
		dataJSON = "{}"
	}
	priorityVal := 1
	if priority != nil {
		priorityVal = *priority
	} else if next, err := r.NextConnectionPriority(provider); err == nil && next > 0 {
		priorityVal = next
	}
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	var nameVal, emailVal any
	if name != "" {
		nameVal = name
	}
	if email != nil {
		emailVal = *email
	}
	now := nowUTC()
	_, err := r.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, provider, authType, nameVal, emailVal, priorityVal, activeInt, dataJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create imported connection: %w", err)
	}
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	if sealed == dataJSON {
		return nil
	}
	if _, err = r.db.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, id); err != nil {
		return fmt.Errorf("create imported connection: redact data: %w", err)
	}
	return nil
}

// UpdateImportedConnection replaces every mutable column of a connection,
// the importer's updateCurrentProviderConnection counterpart.
func (r *Repo) UpdateImportedConnection(id, name string, email *string, priority *int, isActive bool, dataJSON string) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	var nameVal, emailVal any
	if name != "" {
		nameVal = name
	}
	if email != nil {
		emailVal = *email
	}
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	now := nowUTC()
	if _, err = r.db.Exec(
		`UPDATE providerConnections SET name = ?, email = ?, priority = ?, isActive = ?, data = ?, updatedAt = ? WHERE id = ?`,
		nameVal, emailVal, priority, activeInt, sealed, now, id,
	); err != nil {
		return fmt.Errorf("update imported connection %s: %w", id, err)
	}
	return nil
}
