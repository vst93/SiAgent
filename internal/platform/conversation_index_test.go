package platform

import (
	"strings"
	"testing"
)

// TestConversationListUsesIndexes guards the query plan of the sidebar list.
// Without these indexes the list scans every message of every conversation, and
// because the sidebar polls, that cost is paid again on every tick.
func TestConversationListUsesIndexes(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	want := map[string]string{
		"messages_conversation_created":      "messages",
		"messages_conversation_role":         "messages",
		"channel_inbox_conversation_created": "channel_inbox",
	}
	for index, table := range want {
		var got string
		err := f.store.db.QueryRow(
			"SELECT tbl_name FROM sqlite_master WHERE type='index' AND name=?", index,
		).Scan(&got)
		if err != nil || got != table {
			t.Fatalf("index %s missing on %s: %q %v", index, table, got, err)
		}
	}
	// The list query must resolve the newest activity from the index rather than
	// a full scan of the conversation's messages.
	plan := queryPlan(t, f, conversationColumns+`
 WHERE NOT EXISTS(SELECT 1 FROM conversation_cleanup d WHERE d.conversation_id=c.id)
 ORDER BY last_activity DESC, c.rowid DESC`)
	assertPlanUses(t, plan, "messages_conversation_created")
	assertPlanUses(t, plan, "channel_inbox_conversation_created")
}

func queryPlan(t *testing.T, f *fixture, query string) []string {
	t.Helper()
	rows, err := f.store.db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("plan scan: %v", err)
		}
		plan = append(plan, detail)
	}
	return plan
}

func assertPlanUses(t *testing.T, plan []string, index string) {
	t.Helper()
	for _, step := range plan {
		if strings.Contains(step, index) {
			return
		}
	}
	t.Fatalf("query plan does not use %s: %v", index, plan)
}
