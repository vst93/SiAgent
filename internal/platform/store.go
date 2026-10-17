package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"siagent/internal/knowledge"

	_ "modernc.org/sqlite"
)

type Store struct {
	db        *sql.DB
	secrets   cipher.AEAD
	directory string
}

func Open(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(directory, "master.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		// Never create a new key next to an existing database: that would hide a missing-key restore error.
		if _, statErr := os.Stat(filepath.Join(directory, "platform.sqlite")); statErr == nil {
			return nil, errors.New("master.key missing beside existing platform database; restore the complete data directory")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		file, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, createErr
		}
		_, err = file.Write(key)
		if syncErr := file.Sync(); err == nil {
			err = syncErr
		}
		file.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid master.key length")
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "platform.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	absolute, err := filepath.Abs(directory)
	if err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{db: db, secrets: aead, directory: absolute}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(filepath.Join(directory, "platform.sqlite"), 0600); err != nil {
		db.Close()
		return nil, err
	}
	// Verify every saved credential before the Runtime can execute any work.
	rows, err := db.Query("SELECT api_key FROM models UNION ALL SELECT secrets FROM mcps UNION ALL SELECT secrets FROM channels")
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var encrypted string
		if err := rows.Scan(&encrypted); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if _, err := store.decrypt(encrypted); err != nil {
			rows.Close()
			db.Close()
			return nil, fmt.Errorf("cannot decrypt saved credential: restore its matching master.key")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	// Upload staging is never published; abandoned partial uploads are disposable.
	if err := os.RemoveAll(filepath.Join(absolute, ".skill-uploads")); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot clean abandoned skill uploads")
	}
	return store, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) migrate() error {
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 12 {
		return fmt.Errorf("platform database schema %d is newer than supported schema 12", version)
	}
	if version == 12 {
		return nil
	}
	if version == 11 {
		return s.migrateKnowledgeStructure()
	}
	if version == 10 {
		return s.migrateKnowledgeQuality()
	}
	if version == 9 {
		return s.migrateAgentPolicies()
	}
	if version == 8 {
		return s.migrateChannelProviders()
	}
	if version == 7 {
		return s.migrateModelOptions()
	}
	if version == 6 {
		return s.migrateMaintenance()
	}
	if version == 5 {
		return s.migrateChannels()
	}
	if version == 4 {
		return s.migrateAgents()
	}
	if version == 3 {
		return s.migrateKnowledge()
	}
	if version == 2 {
		return s.migrateCapabilities()
	}
	if version == 1 {
		return s.migrateQueue()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE admin(id INTEGER PRIMARY KEY CHECK(id=1),password_hash BLOB NOT NULL);
 CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE models(id INTEGER PRIMARY KEY CHECK(id=1),base_url TEXT NOT NULL,name TEXT NOT NULL,api_key TEXT NOT NULL,version INTEGER NOT NULL);
 CREATE TABLE agents(id TEXT PRIMARY KEY,name TEXT NOT NULL,instructions TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE conversations(id TEXT PRIMARY KEY,title TEXT NOT NULL,created INTEGER NOT NULL);
 CREATE TABLE runs(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),status TEXT NOT NULL,config TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',last_seq INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL);
 CREATE UNIQUE INDEX one_active_run_per_conversation ON runs(conversation_id) WHERE status IN ('running','cancelling');
 CREATE TABLE messages(id INTEGER PRIMARY KEY AUTOINCREMENT,conversation_id TEXT NOT NULL REFERENCES conversations(id),run_id TEXT NOT NULL REFERENCES runs(id),role TEXT NOT NULL,content TEXT NOT NULL,created INTEGER NOT NULL);
 CREATE INDEX messages_conversation ON messages(conversation_id,id);
 PRAGMA user_version=1;
 `)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateQueue()
}
func (s *Store) encrypt(value string) string {
	nonce := make([]byte, s.secrets.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(s.secrets.Seal(nonce, nonce, []byte(value), []byte("siagent:model-key:v1")))
}
func (s *Store) decrypt(value string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	n := s.secrets.NonceSize()
	if len(data) < n {
		return "", errors.New("invalid credential")
	}
	plain, err := s.secrets.Open(nil, data[:n], data[n:], []byte("siagent:model-key:v1"))
	return string(plain), err
}

type Model struct {
	Options  ModelOptions `json:"options"`
	ID       int64        `json:"id"`
	Archived bool         `json:"archived"`
	BaseURL  string       `json:"base_url"`
	Name     string       `json:"name"`
	APIKey   string       `json:"api_key,omitempty"`
	HasKey   bool         `json:"has_key"`
	Version  int          `json:"version"`
}

func (s *Store) model() (Model, error) { return s.modelByID(1) }
func (s *Store) modelByID(id int64) (Model, error) {
	var model Model
	var encrypted, options string
	err := s.db.QueryRow("SELECT id,base_url,name,api_key,version,archived,options FROM models WHERE id=?", id).Scan(&model.ID, &model.BaseURL, &model.Name, &encrypted, &model.Version, &model.Archived, &options)
	if err != nil {
		return model, err
	}
	model.Options, err = parseModelOptions([]byte(options), defaultModelOptions())
	if err != nil {
		return model, err
	}
	model.APIKey, err = s.decrypt(encrypted)
	model.HasKey = model.APIKey != ""
	return model, err
}
func id() string {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

func (s *Store) migrateQueue() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 ALTER TABLE runs ADD COLUMN credential TEXT NOT NULL DEFAULT '';
 ALTER TABLE runs ADD COLUMN released INTEGER NOT NULL DEFAULT 1;
 ALTER TABLE runs ADD COLUMN started INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE runs ADD COLUMN cancel_started INTEGER NOT NULL DEFAULT 0;
 CREATE INDEX runs_queue ON runs(status,created);
 CREATE TABLE run_events(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT NOT NULL REFERENCES runs(id),seq INTEGER NOT NULL,kind TEXT NOT NULL,data TEXT NOT NULL,created INTEGER NOT NULL);
 CREATE UNIQUE INDEX run_event_seq ON run_events(run_id,seq) WHERE seq>0;
 CREATE TABLE tool_calls(run_id TEXT NOT NULL REFERENCES runs(id),call_id TEXT NOT NULL,name TEXT NOT NULL,status TEXT NOT NULL,PRIMARY KEY(run_id,call_id));
 CREATE TABLE settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
 PRAGMA user_version=2;
 `)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateCapabilities()
}

func (s *Store) migrateCapabilities() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE skills(id TEXT PRIMARY KEY,config TEXT NOT NULL,archived INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE mcps(id TEXT PRIMARY KEY,config TEXT NOT NULL,secrets TEXT NOT NULL,version INTEGER NOT NULL,archived INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE agent_capabilities(agent_id TEXT PRIMARY KEY REFERENCES agents(id),config TEXT NOT NULL);
 ALTER TABLE tool_calls ADD COLUMN arguments TEXT NOT NULL DEFAULT '';
 ALTER TABLE tool_calls ADD COLUMN result TEXT NOT NULL DEFAULT '';
 PRAGMA user_version=3;`)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateKnowledge()
}

func (s *Store) migrateKnowledge() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(knowledge.Schema + "\nPRAGMA user_version=4;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateAgents()
}

func (s *Store) migrateAgents() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`ALTER TABLE models RENAME TO models_v4;
 CREATE TABLE models(id INTEGER PRIMARY KEY AUTOINCREMENT,base_url TEXT NOT NULL,name TEXT NOT NULL,api_key TEXT NOT NULL,version INTEGER NOT NULL,archived INTEGER NOT NULL DEFAULT 0);
 INSERT INTO models(id,base_url,name,api_key,version) SELECT id,base_url,name,api_key,version FROM models_v4;
 DROP TABLE models_v4;
 ALTER TABLE agents ADD COLUMN model_id INTEGER NOT NULL DEFAULT 1;
 ALTER TABLE agents ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE conversations ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default';
 CREATE INDEX conversations_agent ON conversations(agent_id,created);
 PRAGMA user_version=5;`)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateChannels()
}

func (s *Store) migrateChannels() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(wecomSchema + "\n PRAGMA user_version=6;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateMaintenance()
}

func (s *Store) migrateMaintenance() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("CREATE TABLE conversation_cleanup(conversation_id TEXT PRIMARY KEY,created INTEGER NOT NULL,attempted INTEGER NOT NULL DEFAULT 0); PRAGMA user_version=7;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateModelOptions()
}
