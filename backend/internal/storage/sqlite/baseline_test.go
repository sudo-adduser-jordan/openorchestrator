package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/domain"
)

// This file covers the baseline schema itself: that a fresh database reaches the
// intended current state, and that a database from the retired 139-file chain is
// refused rather than silently misread.
//
// The per-migration upgrade tests that used to live here are gone with the chain
// they exercised. Each one replayed history to assert a single migration's
// effect; there is no history left to replay. What replaces them is a direct
// assertion on the resulting schema, which is a stronger check anyway: it
// describes the state the code depends on rather than the path taken to reach it.

var expectedUsageTableColumns = map[string][]string{
	"usage_bindings": {
		"id", "session_id", "harness", "native_root_id", "initial_model_id",
		"state", "last_error_code", "updated_at", "provider_hint",
	},
	"usage_sources": {
		"id", "binding_id", "kind", "native_session_id", "subagent_id", "artifact_path",
		"file_identity", "generation", "byte_offset", "parser_state_json", "state",
		"failure_count", "anomaly_count", "next_retry_at", "last_error_code", "updated_at",
	},
	"model_usage_events": {
		"id", "binding_id", "usage_source_id", "provider_id", "billing_provider_id", "model_id",
		"usage_measurement_kind", "input_tokens", "cached_input_tokens",
		"uncached_input_tokens", "output_tokens", "provider_usage_json",
		"source_event_key", "created_at",
		"input_cost_nanos", "cached_input_cost_nanos", "output_cost_nanos",
		"estimated_cost_nanos", "pricing_version",
		"billing_provider_source",
	},
}

// The provider detail tables were folded into one bounded provider usage
// object. Nothing may recreate them.
var retiredUsageTables = []string{"openai_usage_event_details", "anthropic_usage_event_details"}

// openMigratedTestDB opens a fresh database and runs the production migration
// path, so every assertion below sees exactly the schema a new install gets.
func openMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "open-agents.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s columns: %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	return columns
}

// TestBaselineAdmitsEveryShippedHarness guards the one silent-no-op failure the
// hand-maintained baseline makes newly possible: a new migration that is meant
// to widen the sessions.harness CHECK constraint but does not, because the
// target substring drifted. The build still succeeds and migrate() still
// reports success while the schema rejects a harness the product ships. The
// expected set is built from the domain constants so it cannot itself drift.
func TestBaselineAdmitsEveryShippedHarness(t *testing.T) {
	db := openMigratedTestDB(t)

	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, h := range domain.AllHarnesses {
		if !strings.Contains(schema, "'"+string(h)+"'") {
			t.Errorf("sessions.harness CHECK is missing harness %q — the migration that widens it silently no-opped; schema:\n%s", h, schema)
		}
	}
}

// TestBaselineCreatesSessionRevisionFence pins the two schema facts the old
// reconcileSchema pass used to repair at startup. That pass existed because
// burned migration versions could leave the revision trigger missing, which
// silently disables every session compare-and-swap. The baseline creates the
// trigger in one transaction, so the repair is obsolete — but the property it
// protected is not, and a missing trigger is now a broken baseline rather than a
// recoverable one. Assert it is present.
func TestBaselineCreatesSessionRevisionFence(t *testing.T) {
	db := openMigratedTestDB(t)

	var trigger int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'sessions_revision_update'`,
	).Scan(&trigger); err != nil {
		t.Fatalf("inspect session revision trigger: %v", err)
	}
	if trigger != 1 {
		t.Fatal("sessions_revision_update trigger is missing from the baseline; every session compare-and-swap would silently no-op")
	}
}

// TestUsageCdcTriggersSurviveMigrations guards the change_log pipeline for usage
// rows. A table rebuild that drops and recreates usage_bindings/usage_sources
// silently drops their CDC triggers, and nothing fails until change events stop
// firing. Assert all three triggers are present after the full migration chain.
func TestUsageCdcTriggersSurviveMigrations(t *testing.T) {
	db := openMigratedTestDB(t)

	for _, trigger := range []string{
		"usage_sources_cdc_update",
		"usage_bindings_cdc_insert",
		"usage_bindings_cdc_update",
	} {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger,
		).Scan(&count); err != nil {
			t.Fatalf("inspect trigger %s: %v", trigger, err)
		}
		if count != 1 {
			t.Errorf("trigger %s is missing after migration; usage change events would silently stop", trigger)
		}
	}
}

// TestBaselineMigrationVersionsAreUnique keeps the cheap invariant the retired
// chain needed: two migrations sharing a version number make goose apply only
// one of them, silently. With a hand-maintained baseline and hand-appended
// versions this is an easy mistake to make.
func TestBaselineMigrationVersionsAreUnique(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	seen := make(map[string]string, len(entries))
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		version, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			t.Errorf("migration %q does not start with a version number", entry.Name())
			continue
		}
		if previous, dup := seen[version]; dup {
			t.Errorf("migrations %q and %q share version %s; goose applies only one", previous, entry.Name(), version)
		}
		seen[version] = entry.Name()
	}
	if len(entries) == 0 {
		t.Fatal("no embedded migrations found")
	}
	t.Logf("%d embedded migrations: %v", len(names), names)
}

func TestBaselineDefaultsSessionInterfaceToChat(t *testing.T) {
	db := openMigratedTestDB(t)

	var mode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&mode); err != nil {
		t.Fatalf("read default session mode: %v", err)
	}
	if mode != "chat" {
		t.Fatalf("default session mode = %q, want chat", mode)
	}
}

func TestBaselineUsesOpenAgentsUsageMeasurementVocabulary(t *testing.T) {
	db := openMigratedTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config) VALUES ('usage-brand', '/repo/usage-brand', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('usage-brand-1', 'usage-brand', 1, 'opencode', ?, ?, ?);
INSERT INTO usage_bindings (id, session_id, harness, native_root_id, state, updated_at)
VALUES (1, 'usage-brand-1', 'opencode', 'root', 'complete', ?);
INSERT INTO usage_sources (id, binding_id, kind, artifact_path, state, updated_at)
VALUES (1, 1, 'kimi_wire', '/tmp/opencode.jsonl', 'complete', ?);
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, model_id, usage_measurement_kind,
    source_event_key, created_at
) VALUES (1, 1, 'openai', 'gpt-test', 'open_agents_estimated', 'brand-cutover', CURRENT_TIMESTAMP);
`, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed canonical usage event: %v", err)
	}

	var kind string
	if err := db.QueryRow(`SELECT usage_measurement_kind FROM model_usage_events WHERE source_event_key = 'brand-cutover'`).Scan(&kind); err != nil {
		t.Fatalf("read canonical usage event: %v", err)
	}
	if kind != string(domain.UsageMeasurementOpenAgentsEstimated) {
		t.Fatalf("usage measurement kind = %q, want %q", kind, domain.UsageMeasurementOpenAgentsEstimated)
	}
}

func TestBaselineUsageTablesKeepOnlyDurableCollectionState(t *testing.T) {
	db := openMigratedTestDB(t)
	for table, wantColumns := range expectedUsageTableColumns {
		got := tableColumns(t, db, table)
		if !reflect.DeepEqual(got, wantColumns) {
			t.Errorf("%s columns = %v, want %v", table, got, wantColumns)
		}
	}
	for _, table := range retiredUsageTables {
		if got := tableColumns(t, db, table); len(got) != 0 {
			t.Errorf("%s still exists with columns %v", table, got)
		}
	}
}

// TestMigrateRefusesRetiredChainDatabase is the safety contract for the squash.
// A database stamped by the retired 139-file chain records applied version ids
// this build does not declare. goose would treat them as already applied and
// leave the schema exactly as it found it, so the daemon would come up healthy
// against a schema it does not understand. Refusing is the only safe answer.
func TestMigrateRefusesRetiredChainDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "open-agents.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// Stamp a ledger shaped like the retired chain's: version 1 plus a spread of
	// ids from the 2..155 range the baseline no longer declares.
	if _, err := db.Exec(`
CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER, is_applied INTEGER);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1), (22, 1), (86, 1), (155, 1);
`); err != nil {
		t.Fatalf("seed retired-chain ledger: %v", err)
	}

	err = migrate(db)
	if err == nil {
		t.Fatal("migrate accepted a retired-chain database; it must refuse rather than start against an unknown schema")
	}
	for _, want := range []string{"older Open Agents build", "open-agents.db", "155"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q; the user needs an actionable next step", err, want)
		}
	}
}

// TestMigrateAcceptsDatabaseAtKnownVersions proves the guard is set membership
// and not a version threshold: a ledger holding only versions the build
// declares must still be accepted, so appending migration 0002 tomorrow does not
// start rejecting databases.
func TestMigrateAcceptsDatabaseAtKnownVersions(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "open-agents.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER, is_applied INTEGER);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1);
`); err != nil {
		t.Fatalf("seed known-version ledger: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate rejected a ledger holding only declared versions: %v", err)
	}
}

func TestOpenReadOnlyDoesNotCreateDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenReadOnly(context.Background(), dataDir); err == nil {
		t.Fatal("OpenReadOnly succeeded for missing database")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data dir stat err = %v, want not exist", err)
	}
}

// TestOpenReadOnlyDoesNotMigrate keeps the read-only reader honest: it must
// report the mismatch against an out-of-date database rather than quietly
// upgrading it behind the caller's back.
func TestOpenReadOnlyDoesNotMigrate(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    repo_origin_url TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    registered_at TIMESTAMP NOT NULL,
    archived_at TIMESTAMP
);
INSERT INTO projects (id, path, registered_at) VALUES ('alpha', '/repos/alpha', ?);
`, time.Unix(100, 0).UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("seed old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	store, err := OpenReadOnly(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.ListProjects(context.Background()); err == nil {
		t.Fatal("ListProjects succeeded against a pre-baseline schema; want a column failure")
	}

	checkDB, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db")+pragmas)
	if err != nil {
		t.Fatalf("open check db: %v", err)
	}
	defer func() { _ = checkDB.Close() }()

	var schema string
	if err := checkDB.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'",
	).Scan(&schema); err != nil {
		t.Fatalf("read projects schema: %v", err)
	}
	if strings.Contains(schema, "config") || strings.Contains(schema, "kind") {
		t.Fatalf("OpenReadOnly migrated projects schema:\n%s", schema)
	}
}
