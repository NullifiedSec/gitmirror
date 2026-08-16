package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxAttempts = 5

type Event struct {
	Provider   string          `json:"provider,omitempty"`
	Delivery   string          `json:"delivery"`
	Type       string          `json:"type"`
	ReceivedAt time.Time       `json:"received_at"`
	Attempts   int             `json:"attempts"`
	Body       json.RawMessage `json:"body"`
}

type Queue struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Queue { return &Queue{root: root} }

func (q *Queue) Init() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, dir := range []string{"pending", "processing", "done", "failed"} {
		if err := os.MkdirAll(filepath.Join(q.root, "queue", dir), 0o700); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(q.dir("processing"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		from := filepath.Join(q.dir("processing"), entry.Name())
		to := filepath.Join(q.dir("pending"), entry.Name())
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (q *Queue) Enqueue(e Event) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e.Delivery == "" {
		return false, fmt.Errorf("delivery id is required")
	}
	name := safeDeliveryName(e.Delivery) + ".json"
	for _, state := range []string{"pending", "processing", "done"} {
		if _, err := os.Stat(filepath.Join(q.dir(state), name)); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	failedPath := filepath.Join(q.dir("failed"), name)
	if err := os.Remove(failedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, q.write(filepath.Join(q.dir("pending"), name), e)
}

func (q *Queue) Claim() (Event, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := os.ReadDir(q.dir("pending"))
	if err != nil {
		return Event{}, false, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return Event{}, false, nil
	}
	sort.Strings(names)
	name := names[0]
	from := filepath.Join(q.dir("pending"), name)
	to := filepath.Join(q.dir("processing"), name)
	if err := os.Rename(from, to); err != nil {
		return Event{}, false, err
	}
	b, err := os.ReadFile(to)
	if err != nil {
		return Event{}, false, err
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, false, err
	}
	e.Attempts++
	if err := q.write(to, e); err != nil {
		return Event{}, false, err
	}
	return e, true, nil
}

func (q *Queue) Complete(delivery string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := safeDeliveryName(delivery) + ".json"
	return os.Rename(filepath.Join(q.dir("processing"), name), filepath.Join(q.dir("done"), name))
}

func (q *Queue) Fail(e Event) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := safeDeliveryName(e.Delivery) + ".json"
	from := filepath.Join(q.dir("processing"), name)
	state := "pending"
	if e.Attempts >= maxAttempts {
		state = "failed"
	}
	return os.Rename(from, filepath.Join(q.dir(state), name))
}

func (q *Queue) dir(state string) string { return filepath.Join(q.root, "queue", state) }

func (q *Queue) write(path string, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func safeDeliveryName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "delivery"
	}
	return b.String()
}
