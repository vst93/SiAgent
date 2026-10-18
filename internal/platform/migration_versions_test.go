package platform

import (
	"database/sql"
	"fmt"
	"testing"
)

// downgradeSteps removes the artifacts of each migration, newest first. Index i
// downgrades from version i+1 to i.
var downgradeSteps = map[int]string{
	22: `ALTER TABLE api_keys DROP COLUMN secret;`,
	21: `ALTER TABLE knowledge_eval_runs DROP COLUMN kind;`,
	20: `SELECT 1;`,
	19: `DROP TABLE webhook_subscriptions; DROP TABLE webhook_deliveries;`,
	18: `DROP TABLE api_keys; DROP TABLE api_idempotency; DROP TABLE api_conversations; DROP TABLE knowledge_external;
 ALTER TABLE agents DROP COLUMN revision; ALTER TABLE runs DROP COLUMN agent_revision; ALTER TABLE runs DROP COLUMN source;`,
	17: `DROP TABLE plugins; DROP TABLE plugin_events;`,
	16: `DROP TABLE audit_log;`,
	15: `ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs;`,
	14: `DROP TABLE answer_feedback; DROP TABLE knowledge_gaps;`,
	13: `ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;`,
	12: `ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type;
 ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause;
 ALTER TABLE knowledge_chunks DROP COLUMN content_hash;`,
	11: `DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions;`,
}

// downgradeTo walks the schema backwards to target, then leaves the database
// for Open() to migrate forward again.
func downgradeTo(t *testing.T, db *sql.DB, target int) {
	t.Helper()
	for version := 22; version > target; version-- {
		step, ok := downgradeSteps[version]
		if !ok {
			t.Fatalf("missing downgrade step for %d", version)
		}
		if version == 20 {
			dropQualityArtifacts(t, db)
		}
		if _, err := db.Exec(fmt.Sprintf("%s PRAGMA user_version=%d;", step, version-1)); err != nil {
			t.Fatalf("downgrade to %d: %v", version-1, err)
		}
	}
}

func TestUpgradeFromEveryRecentVersionPreservesData(t *testing.T) {
	for target := 10; target <= 21; target++ {
		t.Run(fmt.Sprintf("from-v%d", target), func(t *testing.T) {
			f := newFixture(t)
			f.setup(t)
			f.model(t, "legacy-model", "legacy-secret")
			if _, err := f.store.db.Exec("INSERT INTO knowledge_bases(id,name,description,deleted,created) VALUES('kb','Legacy','',0,1)"); err != nil {
				t.Fatal(err)
			}
			downgradeTo(t, f.store.db, target)
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(f.directory)
			if err != nil {
				t.Fatalf("upgrade from v%d failed: %v", target, err)
			}
			defer upgraded.Close()
			var version int
			if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 22 {
				t.Fatalf("expected schema 22, got %d (%v)", version, err)
			}
			var admin int
			if err := upgraded.db.QueryRow("SELECT count(*) FROM admin").Scan(&admin); err != nil || admin != 1 {
				t.Fatalf("administrator lost: %d %v", admin, err)
			}
			model, err := upgraded.model()
			if err != nil || model.APIKey != "legacy-secret" || model.Name != "legacy-model" {
				t.Fatalf("model lost: %+v %v", model, err)
			}
			var bases int
			if err := upgraded.db.QueryRow("SELECT count(*) FROM knowledge_bases WHERE id='kb'").Scan(&bases); err != nil || bases != 1 {
				t.Fatalf("knowledge base lost: %d %v", bases, err)
			}
			// New columns exist with usable defaults.
			var config string
			if err := upgraded.db.QueryRow("SELECT config FROM knowledge_bases WHERE id='kb'").Scan(&config); err != nil || config == "" {
				t.Fatalf("config column missing: %q %v", config, err)
			}
		})
	}
}

// dropQualityArtifacts removes the columns and tables added after schema 19.
// It checks existence first because some downgrade paths already dropped the
// knowledge tables.
func dropQualityArtifacts(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"knowledge_conflicts", "knowledge_vocabulary", "run_evidence"} {
		_, _ = db.Exec("DROP TABLE IF EXISTS " + table)
	}
	dropColumn := func(table, column string) {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return
		}
		found := false
		for rows.Next() {
			var cid int
			var name, kind string
			var notnull int
			var dflt any
			var pk int
			if rows.Scan(&cid, &name, &kind, &notnull, &dflt, &pk) == nil && name == column {
				found = true
			}
		}
		rows.Close()
		if found {
			_, _ = db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column)
		}
	}
	dropColumn("knowledge_bases", "config")
	dropColumn("knowledge_eval_questions", "answerable")
	dropColumn("knowledge_eval_questions", "expected_answer")
	dropColumn("knowledge_eval_runs", "kind")
	dropColumn("plugins", "config")
	dropColumn("plugins", "secrets")
	dropColumn("plugins", "health")
	dropColumn("plugins", "last_error")
}
