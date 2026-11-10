package cli

import (
	"testing"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

// A scheduled_due item is the task itself: its id is the task id, and the
// listener's line and JSON must say so (task ref), like any task item.
func TestCollectItemsScheduledDue(t *testing.T) {
	data := js.O("scheduled_due", []any{js.O(
		"id", "t9", "title", "Follow up", "status", "todo", "scheduled_date", "2026-11-17",
		"due_since", "2026-11-17T05:00:01Z", "for", "self",
	)})
	items := collectItems("alpha", data)
	if len(items) != 1 {
		t.Fatalf("items %v", items)
	}
	if v, _ := items[0].Get("task_id"); v != "t9" {
		t.Fatalf("task_id %v", v)
	}
	if got := itemLine(items[0]); got != `alpha  scheduled_due  t9  task t9  2026-11-17T05:00:01Z  "Follow up"` {
		t.Fatalf("line %q", got)
	}
}
