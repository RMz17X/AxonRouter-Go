package db

// tableDef mirrors one entry of upstream schema.js TABLES: the
// declarative current schema. Column definitions are kept in the shape
// the queries in this package write against.
type tableDef struct {
	name       string
	columns    [][2]string // {name, definition}
	primaryKey string      // extra table-level constraint, e.g. PRIMARY KEY (scope, key)
	indexes    []string
}

// PostgreSQL schema definitions: the gateway's full table set.
//
// Rules applied throughout:
//   - Identifiers stay unquoted, exactly as every query in this package
//     writes them. PostgreSQL folds unquoted identifiers to lowercase
//     in DDL and DML alike, so the folded catalog names keep matching
//     the unquoted references the existing code makes.
//   - TEXT stays text, except the JSON payload columns listed below.
//   - INTEGER counters (tokens, bytes, counts, epoch seconds, row-id
//     references) become bigint; small flags, priorities and limits stay
//     integer. REAL becomes double precision. BLOB becomes bytea.
//   - AUTOINCREMENT becomes a bigint identity column (see the
//     partitioned parents in schema_pg.go for the two hot tables).
//   - JSON payloads become jsonb where the codebase treats the column
//     as a JSON document end to end: the two columns SQL itself
//     extracts from (requestDetails.data, usageHistory.tokens — every
//     JSON extraction in the package targets one of them), plus the
//     whole-document payload columns AxonRouter's schema.pg.js also
//     declares jsonb and this code only ever writes through
//     json.Marshal (settings.data, providerConnections.data,
//     providerNodes.data, proxyPools.data, combos.models,
//     usageDaily.data, usageHistory.meta). Columns whose write contract
//     admits non-JSON text (kv.value, apiKeys.metadata with its ”
//     default, combos.matchRules, guardrail config) stay text: a jsonb
//     column would reject values those contracts allow.
func pgCoreSchema() []tableDef {
	return []tableDef{
		{
			name: "settings",
			columns: [][2]string{
				{"id", "INTEGER PRIMARY KEY CHECK (id = 1)"},
				{"data", "JSONB NOT NULL"},
			},
		},
		{
			name: "_meta",
			columns: [][2]string{
				{"key", "TEXT PRIMARY KEY"},
				{"value", "TEXT NOT NULL"},
			},
		},
		{
			name: "providerConnections",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"provider", "TEXT NOT NULL"},
				{"authType", "TEXT NOT NULL"},
				{"name", "TEXT"},
				{"email", "TEXT"},
				{"priority", "INTEGER"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"data", "JSONB NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pc_provider ON providerConnections(provider)",
				"CREATE INDEX IF NOT EXISTS idx_pc_provider_active ON providerConnections(provider, isActive)",
				"CREATE INDEX IF NOT EXISTS idx_pc_priority ON providerConnections(provider, priority)",
				"CREATE INDEX IF NOT EXISTS idx_pc_sort_order ON providerConnections ((CASE WHEN priority IS NULL THEN 999999 ELSE priority END) ASC, updatedAt DESC)",
				"CREATE INDEX IF NOT EXISTS idx_pc_prov_act_sort ON providerConnections (provider, isActive, (CASE WHEN priority IS NULL THEN 999999 ELSE priority END) ASC, updatedAt DESC)",
				"CREATE INDEX IF NOT EXISTS idx_pc_last_used_at ON providerConnections(lastUsedAt DESC NULLS LAST)",
			},
		},
		{
			name: "providerNodes",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"type", "TEXT"},
				{"name", "TEXT"},
				{"data", "JSONB NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pn_type ON providerNodes(type)",
			},
		},
		{
			name: "proxyPools",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"testStatus", "TEXT"},
				{"data", "JSONB NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pp_active ON proxyPools(isActive)",
				"CREATE INDEX IF NOT EXISTS idx_pp_status ON proxyPools(testStatus)",
			},
		},
		{
			name: "proxyGroups",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"name", "TEXT"},
				{"data", "JSONB NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pg_name ON proxyGroups(name)",
			},
		},
		{
			name: "apiKeys",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"key", "TEXT UNIQUE NOT NULL"},
				{"name", "TEXT"},
				{"machineId", "TEXT"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"createdAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_ak_key ON apiKeys(key)",
				"CREATE INDEX IF NOT EXISTS idx_ak_lookup ON apiKeys(lookupHash)",
			},
		},
		{
			name: "combos",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"name", "TEXT UNIQUE NOT NULL"},
				{"kind", "TEXT"},
				{"models", "JSONB NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_combo_name ON combos(name)",
			},
		},
		{
			name: "kv",
			columns: [][2]string{
				{"scope", "TEXT NOT NULL"},
				{"key", "TEXT NOT NULL"},
				{"value", "TEXT NOT NULL"},
			},
			primaryKey: "PRIMARY KEY (scope, key)",
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_kv_scope ON kv(scope)",
			},
		},
		{
			name: "usageDaily",
			columns: [][2]string{
				{"dateKey", "TEXT PRIMARY KEY"},
				{"data", "JSONB NOT NULL"},
			},
		},
		{
			name: "compressionAnalytics",
			columns: [][2]string{
				{"id", "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"mode", "TEXT NOT NULL"},
				{"originalTokens", "BIGINT NOT NULL"},
				{"compressedTokens", "BIGINT NOT NULL"},
				{"tokensSaved", "BIGINT NOT NULL"},
				{"durationMs", "BIGINT DEFAULT 0"},
				{"requestId", "TEXT"},
				{"actualPromptTokens", "BIGINT DEFAULT 0"},
				{"actualCompletionTokens", "BIGINT DEFAULT 0"},
				{"actualTotalTokens", "BIGINT DEFAULT 0"},
				{"actualCacheReadTokens", "BIGINT DEFAULT 0"},
				{"actualCacheWriteTokens", "BIGINT DEFAULT 0"},
				{"estimatedUsdSaved", "DOUBLE PRECISION DEFAULT 0"},
				{"skipReason", "TEXT"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_ca_ts ON compressionAnalytics(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_ca_provider ON compressionAnalytics(provider)",
				"CREATE INDEX IF NOT EXISTS idx_ca_model ON compressionAnalytics(model)",
				"CREATE INDEX IF NOT EXISTS idx_ca_mode ON compressionAnalytics(mode)",
				"CREATE INDEX IF NOT EXISTS idx_ca_req ON compressionAnalytics(requestId)",
				"CREATE INDEX IF NOT EXISTS idx_ca_stat_p ON compressionAnalytics(provider, timestamp, tokensSaved, originalTokens, durationMs)",
				"CREATE INDEX IF NOT EXISTS idx_ca_stat_m ON compressionAnalytics(model, timestamp, tokensSaved, originalTokens, durationMs)",
				"CREATE INDEX IF NOT EXISTS idx_ca_saved ON compressionAnalytics(tokensSaved, timestamp)",
				"CREATE INDEX IF NOT EXISTS idx_ca_receipt ON compressionAnalytics(timestamp, actualPromptTokens, actualTotalTokens)",
			},
		},
		{
			name: "semanticCacheEntries",
			columns: [][2]string{
				{"key", "TEXT PRIMARY KEY"},
				{"model", "TEXT NOT NULL"},
				{"responseBody", "BYTEA NOT NULL"},
				{"contentType", "TEXT"},
				{"storedAt", "TEXT NOT NULL"},
				{"hitCount", "BIGINT DEFAULT 0"},
				{"tokensSaved", "BIGINT DEFAULT 0"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_sce_model ON semanticCacheEntries(model)",
				"CREATE INDEX IF NOT EXISTS idx_sce_stored ON semanticCacheEntries(storedAt)",
			},
		},
		{
			name: "files",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"filename", "TEXT NOT NULL"},
				{"bytes", "BIGINT NOT NULL"},
				{"purpose", "TEXT NOT NULL"},
				{"created_at", "BIGINT NOT NULL"},
				{"content", "BYTEA NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_files_purpose ON files(purpose)",
				"CREATE INDEX IF NOT EXISTS idx_files_created ON files(created_at DESC)",
			},
		},
		{
			name: "batches",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"input_file_id", "TEXT NOT NULL"},
				{"endpoint", "TEXT NOT NULL"},
				{"completion_window", "TEXT NOT NULL"},
				{"status", "TEXT NOT NULL"},
				{"output_file_id", "TEXT"},
				{"error_file_id", "TEXT"},
				{"created_at", "BIGINT NOT NULL"},
				{"completed_at", "BIGINT"},
				{"cancelled_at", "BIGINT"},
				{"metadata", "TEXT"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status)",
				"CREATE INDEX IF NOT EXISTS idx_batches_created ON batches(created_at DESC)",
				"CREATE INDEX IF NOT EXISTS idx_batches_input_file ON batches(input_file_id)",
			},
		},
		{
			// Batch 7 (AxonRouter parity): mcp_tool_audit, PostgreSQL dialect.
			// created_at stays TEXT (ISO strings, string-compared by the
			// stats window) and the writer always supplies it, so no
			// database-side datetime() default is declared.
			name: "mcp_tool_audit",
			columns: [][2]string{
				{"id", "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY"},
				{"tool_name", "TEXT NOT NULL"},
				{"input_hash", "TEXT NOT NULL"},
				{"output_summary", "TEXT"},
				{"duration_ms", "BIGINT NOT NULL DEFAULT 0"},
				{"api_key_id", "TEXT"},
				{"transport", "TEXT"},
				{"success", "INTEGER NOT NULL DEFAULT 1"},
				{"error_code", "TEXT"},
				{"created_at", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_mcp_tool_audit_created ON mcp_tool_audit(created_at DESC)",
				"CREATE INDEX IF NOT EXISTS idx_mcp_tool_audit_tool ON mcp_tool_audit(tool_name)",
			},
		},
	}
}

// pgKeiRouterTables mirrors keiRouterTables with PostgreSQL types.
func pgKeiRouterTables() []tableDef {
	return []tableDef{
		{
			name: "plans",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"tenant_id", "TEXT NOT NULL DEFAULT 'default'"},
				{"name", "TEXT NOT NULL"},
				{"description", "TEXT DEFAULT ''"},
				{"limit_micros", "BIGINT NOT NULL DEFAULT 0"},
				{"limit_tokens", "BIGINT NOT NULL DEFAULT 0"},
				{"rpm_limit", "INTEGER NOT NULL DEFAULT 0"},
				{"tpm_limit", "INTEGER NOT NULL DEFAULT 0"},
				{"concurrency_limit", "INTEGER NOT NULL DEFAULT 0"},
				{"period", "TEXT NOT NULL DEFAULT 'monthly'"},
				{"alert_pct", "INTEGER NOT NULL DEFAULT 80"},
				{"hard_cutoff", "INTEGER NOT NULL DEFAULT 1"},
				{"allowed_models", "TEXT DEFAULT ''"},
				{"created_at", "TEXT NOT NULL"},
				{"updated_at", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_plans_tenant ON plans(tenant_id)",
			},
		},
		{
			name: "api_key_model_access",
			columns: [][2]string{
				{"api_key_id", "TEXT NOT NULL"},
				{"model", "TEXT NOT NULL"},
				{"created_at", "TEXT NOT NULL"},
			},
			primaryKey: "PRIMARY KEY (api_key_id, model)",
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_akma_key ON api_key_model_access(api_key_id)",
			},
		},
		{
			name: "guardrail_policies",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"tenant_id", "TEXT NOT NULL DEFAULT 'default'"},
				{"scope", "TEXT NOT NULL"},
				{"scope_id", "TEXT NOT NULL DEFAULT ''"},
				{"name", "TEXT NOT NULL"},
				{"enabled", "INTEGER NOT NULL DEFAULT 1"},
				{"config", "TEXT NOT NULL DEFAULT '{}'"},
				{"created_at", "TEXT NOT NULL"},
				{"updated_at", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE UNIQUE INDEX IF NOT EXISTS idx_grp_scope ON guardrail_policies(tenant_id, scope, scope_id)",
			},
		},
		{
			name: "guardrail_logs",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"tenant_id", "TEXT NOT NULL"},
				{"request_id", "TEXT DEFAULT ''"},
				{"api_key_id", "TEXT DEFAULT ''"},
				{"provider", "TEXT DEFAULT ''"},
				{"model", "TEXT DEFAULT ''"},
				{"chain_id", "TEXT DEFAULT ''"},
				{"detector", "TEXT NOT NULL"},
				{"direction", "TEXT NOT NULL"},
				{"action", "TEXT NOT NULL"},
				{"severity", "TEXT DEFAULT ''"},
				{"reason", "TEXT DEFAULT ''"},
				{"findings", "TEXT DEFAULT '[]'"},
				{"created_at", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_grl_ts ON guardrail_logs(created_at DESC)",
			},
		},
	}
}

// pgUsageLedgerTables mirrors usageLedgerTables with PostgreSQL types.
func pgUsageLedgerTables() []tableDef {
	return []tableDef{
		{
			name: "usageLedgerCarryover",
			columns: [][2]string{
				{"keyId", "TEXT PRIMARY KEY"},
				{"tokens", "BIGINT NOT NULL DEFAULT 0"},
				{"updatedAt", "TEXT NOT NULL"},
			},
		},
		{
			name: "providerLedgerCarryover",
			columns: [][2]string{
				{"providerId", "TEXT PRIMARY KEY"},
				{"tokens", "BIGINT NOT NULL DEFAULT 0"},
				{"updatedAt", "TEXT NOT NULL"},
			},
		},
	}
}

// pgGoOnlyColumns lists the additive Go-runtime columns with
// PostgreSQL types. The bootstrap applies them with the same
// skip-if-table-absent semantics as the rest of the additive pass.
var pgGoOnlyColumns = [][3]string{
	{"providerConnections", "lastUsedAt", "TEXT"},
	{"providerConnections", "consecutiveUseCount", "BIGINT DEFAULT 0"},
	{"apiKeys", "rateLimitRPM", "INTEGER DEFAULT 0"},
	{"apiKeys", "rateLimitTPM", "INTEGER DEFAULT 0"},
	{"apiKeys", "rateLimitConcurrency", "INTEGER DEFAULT 0"},
	{"apiKeys", "tokenQuota", "BIGINT DEFAULT 0"},
	{"apiKeys", "queueTimeoutMs", "INTEGER DEFAULT 0"},
	{"apiKeys", "keyHash", "TEXT DEFAULT ''"},
	{"apiKeys", "lookupHash", "TEXT DEFAULT ''"},
	{"apiKeys", "keyDisplay", "TEXT DEFAULT ''"},
	{"apiKeys", "expiresAt", "TEXT DEFAULT ''"},
	{"apiKeys", "lastUsedAt", "TEXT DEFAULT ''"},
	{"apiKeys", "usedCount", "BIGINT DEFAULT 0"},
	{"apiKeys", "metadata", "TEXT DEFAULT ''"},
	{"apiKeys", "quotaResetId", "BIGINT DEFAULT 0"},
	{"usageHistory", "clientKey", "TEXT DEFAULT ''"},
	{"apiKeys", "planId", "TEXT DEFAULT ''"},
	{"apiKeys", "limitMicros", "BIGINT DEFAULT 0"},
	{"apiKeys", "limitTokens", "BIGINT DEFAULT 0"},
	{"apiKeys", "budgetPeriod", "TEXT DEFAULT ''"},
	{"apiKeys", "alertPct", "INTEGER DEFAULT 0"},
	{"apiKeys", "hardCutoff", "INTEGER DEFAULT 0"},
	{"providerNodes", "rpmLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "tpmLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "concurrencyLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "tokenQuota", "BIGINT DEFAULT 0"},
	{"providerNodes", "queueTimeoutMs", "INTEGER DEFAULT 0"},
	{"providerNodes", "quotaResetId", "BIGINT DEFAULT 0"},
	{"providerNodes", "contractExpiresAt", "TEXT DEFAULT ''"},
	{"combos", "matchRules", "TEXT DEFAULT '[]'"},
}
