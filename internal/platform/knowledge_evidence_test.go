package platform

import (
	"testing"
)

func evidenceFixture(t *testing.T, answer string, withTool bool, sources int) *fixture {
	t.Helper()
	f := newFixture(t)
	f.setup(t)
	if _, err := f.store.db.Exec("INSERT INTO conversations(id,title,created,agent_id) VALUES('c','t',1,'default'); INSERT INTO runs(id,conversation_id,status,config,created) VALUES('r','c','completed','{}',1);"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("INSERT INTO messages(conversation_id,run_id,role,content,created) VALUES('c','r','assistant',?,1)", answer); err != nil {
		t.Fatal(err)
	}
	if withTool {
		if _, err := f.store.db.Exec("INSERT INTO tool_calls(run_id,call_id,name,status) VALUES('r','call-1','knowledge_search','completed')"); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < sources; index++ {
		if _, err := f.store.db.Exec("INSERT INTO run_sources(run_id,source_id,reference) VALUES('r',?,?)", "source-"+string(rune('a'+index)), `{"title":"policy"}`); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordAnswerEvidence(tx, "r"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAnswerEvidenceMarksGroundedAnswers(t *testing.T) {
	f := evidenceFixture(t, "住宿标准为每晚 500 元 [1]。", true, 1)
	var state string
	var citation int
	if err := f.store.db.QueryRow("SELECT evidence_state,citation_valid FROM runs WHERE id='r'").Scan(&state, &citation); err != nil {
		t.Fatal(err)
	}
	if state != "used" || citation != 1 {
		t.Fatalf("grounded answer flagged incorrectly: %s %d", state, citation)
	}
}

func TestAnswerEvidenceMarksMissingEvidence(t *testing.T) {
	f := evidenceFixture(t, "资料中没有提到。", true, 0)
	var state string
	var citation int
	if err := f.store.db.QueryRow("SELECT evidence_state,citation_valid FROM runs WHERE id='r'").Scan(&state, &citation); err != nil {
		t.Fatal(err)
	}
	if state != "none" || citation != -1 {
		t.Fatalf("missing evidence flagged incorrectly: %s %d", state, citation)
	}
}

func TestAnswerEvidenceRejectsUnknownCitation(t *testing.T) {
	f := evidenceFixture(t, "住宿标准见 [3]。", true, 1)
	var citation int
	if err := f.store.db.QueryRow("SELECT citation_valid FROM runs WHERE id='r'").Scan(&citation); err != nil {
		t.Fatal(err)
	}
	if citation != 0 {
		t.Fatalf("invalid citation accepted: %d", citation)
	}
}

func TestAnswerEvidenceIgnoresRunsWithoutKnowledge(t *testing.T) {
	f := evidenceFixture(t, "普通回答 [9]。", false, 0)
	var state string
	var citation int
	if err := f.store.db.QueryRow("SELECT evidence_state,citation_valid FROM runs WHERE id='r'").Scan(&state, &citation); err != nil {
		t.Fatal(err)
	}
	if state != "" || citation != 0 {
		t.Fatalf("non-knowledge run should not claim evidence state: %q %d", state, citation)
	}
}

func TestAnswerEvidenceIsExposedInSnapshot(t *testing.T) {
	f := evidenceFixture(t, "住宿标准为每晚 500 元 [1]。", true, 1)
	snapshot, err := f.server.snapshot("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].EvidenceState != "used" || snapshot.Runs[0].CitationValid != 1 {
		t.Fatalf("snapshot missing evidence fields: %+v", snapshot.Runs)
	}
}
