package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MRState holds optional per-merge-request progress for incremental CI runs.
type MRState struct {
	LastReviewedSHA string    `json:"last_reviewed_sha,omitempty"`
	WalkthroughID   int64     `json:"walkthrough_note_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type diskState struct {
	MergeRequests map[string]MRState `json:"merge_requests"`
}

// Store is a small JSON file used as optional CI cache for last reviewed SHA
// and walkthrough note IDs. It is not a durable job queue.
type Store struct {
	mu   sync.Mutex
	path string
	data diskState
}

func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: diskState{MergeRequests: map[string]MRState{}},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func Key(projectID, iid int64) string {
	return fmt.Sprintf("%d:%d", projectID, iid)
}

func (s *Store) Get(projectID, iid int64) MRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.MergeRequests[Key(projectID, iid)]
}

func (s *Store) Update(projectID, iid int64, fn func(*MRState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(projectID, iid)
	value := s.data.MergeRequests[key]
	fn(&value)
	value.UpdatedAt = time.Now().UTC()
	s.data.MergeRequests[key] = value
	return s.persistLocked()
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	if s.data.MergeRequests == nil {
		s.data.MergeRequests = map[string]MRState{}
	}
	return nil
}

func (s *Store) persistLocked() error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.data); err != nil {
		temporary.Close()
		return fmt.Errorf("encode state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
