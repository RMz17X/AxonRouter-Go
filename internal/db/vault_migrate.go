// Credential migration (F-5 §5.6).
//
// Sealing on write only covers credentials stored after the vault was switched
// on. An install that has been running for months holds every provider token
// in plaintext `data`, and the vault reports those rows as plaintext forever
// unless something walks them. This is that walk.
//
// The pass is deliberately fail-safe rather than fail-closed. Losing the master
// key already means the credentials are unrecoverable (F-5 §9), so a migration
// that aborts the boot on one bad row would trade a recoverable problem for an
// unbootable one: a row that cannot be sealed keeps its plaintext and the
// gateway starts, because a running gateway with one unsealed row is strictly
// better than a gateway that refuses to start.
package db

import (
	"context"
	"fmt"

	"axonrouter/proxy/internal/log"
)

// SealResult reports what a migration pass did.
type SealResult struct {
	// Sealed is the number of connections whose credentials moved into the
	// sealed columns.
	Sealed int
	// Skipped names connections that were left alone, with the reason. A row
	// appears here when its payload is not a JSON object, when it holds no
	// whitelisted credential, or when sealing it failed.
	Skipped []string
}

// MigratePlaintextCredentials seals every connection that still holds a
// plaintext credential.
//
// There is no pre-migration snapshot: the database is a server, not a
// file this process owns, so the restore point before sealing rewrites
// credential rows is the server's own backup — the operator's side of
// the deployment. The pass says so in the log before its first write.
//
// A vault-less Repo is a no-op, so an install without ROUTER_MASTER_KEY pays
// nothing and keeps behaving exactly as before.
func (r *Repo) MigratePlaintextCredentials(ctx context.Context) (SealResult, error) {
	var result SealResult
	if !r.sealingEnabled() {
		return result, nil
	}

	candidates, err := r.ListConnectionsNeedingSeal(ctx)
	if err != nil {
		return result, err
	}
	if len(candidates) == 0 {
		return result, nil
	}

	// No snapshot precedes the first write (see the function comment):
	// the restore point is the server's backup, and the log records
	// that the pass is about to rewrite credential rows without one.
	log.Info("vault", "sealing plaintext credentials; restore point is the database server's backup")

	for _, c := range candidates {
		sealed, sErr := r.sealOnWrite(c.ID, c.Data)
		if sErr != nil {
			// One bad row must not stop the pass: the remaining connections
			// still need sealing, and this one keeps working plaintext.
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %v", c.ID, sErr))
			log.Warn("vault", "connection not sealed, left plaintext", "conn", c.ID, "error", sErr)
			continue
		}
		if sealed == c.Data {
			// Nothing was extracted, so this row is not actually a candidate;
			// listing it would overstate what the migration did.
			continue
		}
		// sealOnWrite returns the redacted payload; the row itself still holds
		// the credential until it is rewritten here.
		if _, wErr := r.ExecContext(ctx, `UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, c.ID); wErr != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %v", c.ID, wErr))
			log.Warn("vault", "sealed credentials not redacted in place", "conn", c.ID, "error", wErr)
			continue
		}
		result.Sealed++
	}

	log.Info("vault", "plaintext credential migration", "sealed", result.Sealed, "skipped", len(result.Skipped))
	return result, nil
}
