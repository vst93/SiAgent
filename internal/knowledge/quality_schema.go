package knowledge

// QualitySchema20 adds per-base retrieval config, conflict/vocabulary/evidence
// tables and answerability columns. Additive only.
const QualitySchema20 = `
 ALTER TABLE knowledge_bases ADD COLUMN config TEXT NOT NULL DEFAULT '{}';
 ALTER TABLE knowledge_eval_questions ADD COLUMN answerable INTEGER NOT NULL DEFAULT 1;
 ALTER TABLE knowledge_eval_questions ADD COLUMN expected_answer TEXT NOT NULL DEFAULT '';
 CREATE TABLE knowledge_conflicts(id INTEGER PRIMARY KEY AUTOINCREMENT,knowledge_id TEXT NOT NULL,document_a TEXT NOT NULL,document_b TEXT NOT NULL,detail TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'open',created INTEGER NOT NULL);
 CREATE TABLE knowledge_vocabulary(knowledge_id TEXT NOT NULL,term TEXT NOT NULL,expansion TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(knowledge_id,term));
 CREATE TABLE run_evidence(run_id TEXT NOT NULL,claim_index INTEGER NOT NULL,source_ids TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(run_id,claim_index));
`

// QualitySchema21 marks evaluation runs as retrieval or answer quality.
const QualitySchema21 = `
 ALTER TABLE knowledge_eval_runs ADD COLUMN kind TEXT NOT NULL DEFAULT 'retrieval';
`
