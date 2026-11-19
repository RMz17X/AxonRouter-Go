package db_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"axonrouter/proxy/internal/db"
)

// TestClientUsagePageCooldownExpiry pins the status filter to the same
// definition of a live model lock the serving path uses: an expired
// RFC3339 lock is not a cooldown, so a recovered account belongs in the
// active bucket. The SQL filter used to match on the lock key alone,
// which kept an account inactive forever after any model failure.
func TestClientUsagePageCooldownExpiry(t *testing.T) {
	repo := pgBulkRepo(t)
	ctx := context.Background()
	live := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	rows := []struct {
		id       string
		active   int
		lockTime string
	}{
		{id: "conn-live-lock", active: 1, lockTime: live},
		{id: "conn-expired-lock", active: 1, lockTime: expired},
		{id: "conn-disabled", active: 0},
		{id: "conn-clean", active: 1},
	}
	for _, row := range rows {
		var lock any
		if row.lockTime != "" {
			lock = row.lockTime
		}
		_, err := repo.RawDB().ExecContext(ctx, `
			INSERT INTO providerConnections (id, provider, authType, name, data, isActive, createdAt, updatedAt)
			VALUES ($1, 'deepseek', 'api-key', $1,
			        CASE WHEN $3::text IS NULL THEN '{}'::jsonb
			             ELSE jsonb_build_object('modelLock_gemini-2.5-pro', $3::text) END,
			        $2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			row.id, row.active, lock)
		if err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}

	tests := []struct {
		name string
		want []string
	}{
		{
			name: "active",
			want: []string{"conn-clean", "conn-expired-lock"},
		},
		{
			name: "inactive",
			want: []string{"conn-disabled", "conn-live-lock"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := repo.GetClientUsagePage(ctx, db.ClientUsagePageFilter{
				Supported:     []string{"deepseek"},
				APIKey:        []string{"deepseek"},
				AccountStatus: tt.name,
				Page:          1,
				PageSize:      10,
			})
			if err != nil {
				t.Fatalf("GetClientUsagePage: %v", err)
			}
			got := make([]string, 0, len(page.Connections))
			for _, conn := range page.Connections {
				got = append(got, conn.ID)
			}
			sort.Strings(got)
			if len(got) != len(tt.want) {
				t.Fatalf("connection ids = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("connection ids = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
