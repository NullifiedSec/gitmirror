package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQueueLifecycleAndIdempotency(t *testing.T) {
	q := New(t.TempDir())
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	e := Event{Delivery: "abc-123", Type: "push", ReceivedAt: time.Now().UTC(), Body: json.RawMessage(`{"ok":true}`)}
	accepted, err := q.Enqueue(e)
	if err != nil || !accepted {
		t.Fatalf("first enqueue accepted=%v err=%v", accepted, err)
	}
	accepted, err = q.Enqueue(e)
	if err != nil || accepted {
		t.Fatalf("duplicate enqueue accepted=%v err=%v", accepted, err)
	}
	claimed, ok, err := q.Claim()
	if err != nil || !ok || claimed.Attempts != 1 {
		t.Fatalf("claim ok=%v attempts=%d err=%v", ok, claimed.Attempts, err)
	}
	if err := q.Fail(claimed); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = q.Claim()
	if err != nil || !ok || claimed.Attempts != 2 {
		t.Fatalf("retry claim ok=%v attempts=%d err=%v", ok, claimed.Attempts, err)
	}
	if err := q.Complete(claimed.Delivery); err != nil {
		t.Fatal(err)
	}
	accepted, err = q.Enqueue(e)
	if err != nil || accepted {
		t.Fatalf("completed delivery should remain idempotent: accepted=%v err=%v", accepted, err)
	}
}
