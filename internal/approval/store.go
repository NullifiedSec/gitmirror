package approval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Request struct {
	ID             string    `json:"id"`
	Pair           string    `json:"pair"`
	SourceProvider string    `json:"source_provider"`
	SourceFullName string    `json:"source_full_name"`
	TargetProvider string    `json:"target_provider"`
	TargetFullName string    `json:"target_full_name"`
	Ref            string    `json:"ref"`
	Before         string    `json:"before"`
	After          string    `json:"after"`
	Delete         bool      `json:"delete"`
	CreatedAt      time.Time `json:"created_at"`
}

type Store struct {
	root string
}

func New(dataDir string) *Store {
	return &Store{root: filepath.Join(dataDir, "approvals")}
}

func (s *Store) Create(req Request) (Request, error) {
	if req.ID == "" {
		id, err := randomID()
		if err != nil {
			return Request{}, err
		}
		req.ID = id
	}
	if !validID(req.ID) {
		return Request{}, fmt.Errorf("invalid approval id")
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Join(s.root, "pending"), 0o700); err != nil {
		return Request{}, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return Request{}, err
	}
	path := filepath.Join(s.root, "pending", req.ID+".json")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (s *Store) Load(id string) (Request, error) {
	if !validID(id) {
		return Request{}, fmt.Errorf("invalid approval id")
	}
	b, err := os.ReadFile(filepath.Join(s.root, "pending", id+".json"))
	if err != nil {
		return Request{}, err
	}
	var req Request
	if err := json.Unmarshal(b, &req); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (s *Store) ListPending() ([]Request, error) {
	dir := filepath.Join(s.root, "pending")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]Request, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		req, err := s.Load(id)
		if err != nil {
			return nil, fmt.Errorf("load pending approval %s: %w", id, err)
		}
		items = append(items, req)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Store) Complete(id string) error {
	return s.move(id, "done")
}

func (s *Store) Reject(id string) error {
	return s.move(id, "rejected")
}

func (s *Store) move(id, state string) error {
	if !validID(id) {
		return fmt.Errorf("invalid approval id")
	}
	if err := os.MkdirAll(filepath.Join(s.root, state), 0o700); err != nil {
		return err
	}
	from := filepath.Join(s.root, "pending", id+".json")
	to := filepath.Join(s.root, state, id+".json")
	if err := os.Rename(from, to); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("approval %s is no longer pending", id)
		}
		return err
	}
	return nil
}

func randomID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
