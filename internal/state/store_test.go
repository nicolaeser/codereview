package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsLastReviewedSHA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(7, 11, func(value *MRState) {
		value.LastReviewedSHA = "abc123"
		value.WalkthroughID = 99
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get(7, 11)
	if got.LastReviewedSHA != "abc123" || got.WalkthroughID != 99 {
		t.Fatalf("reloaded state = %#v", got)
	}
}

func TestLoadLegacyStateIgnoresUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := writeFile(path, `{
  "merge_requests": {"1:2": {"last_reviewed_sha": "deadbeef", "paused": true, "auto_review_count": 3}},
  "webhook_ids": {"x": "2026-01-01T00:00:00Z"},
  "jobs": {"job-1": {"id": "job-1", "status": "queued"}}
}`); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get(1, 2)
	if got.LastReviewedSHA != "deadbeef" {
		t.Fatalf("legacy MR state = %#v", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
