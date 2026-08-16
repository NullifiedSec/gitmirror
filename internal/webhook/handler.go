package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/NullifiedSec/gitmirror/internal/queue"
)

const maxBody = 10 << 20

const (
	ProviderGitHub = "github"
	ProviderGitea  = "gitea"
)

type Enqueuer interface {
	Enqueue(queue.Event) (bool, error)
}

type Handler struct {
	provider string
	secret   []byte
	queue    Enqueuer
}

func New(provider, secret string, q Enqueuer) *Handler {
	return &Handler{provider: strings.ToLower(strings.TrimSpace(provider)), secret: []byte(secret), queue: q}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	delivery, eventType, signature, err := h.headers(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !Verify(h.secret, body, signature) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	accepted, err := h.queue.Enqueue(queue.Event{
		Provider:   h.provider,
		Delivery:   delivery,
		Type:       eventType,
		ReceivedAt: time.Now().UTC(),
		Body:       body,
	})
	if err != nil {
		http.Error(w, "failed to persist event", http.StatusInternalServerError)
		return
	}
	if !accepted {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "duplicate\n")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "accepted\n")
}

func (h *Handler) headers(r *http.Request) (delivery, eventType, signature string, err error) {
	switch h.provider {
	case ProviderGitHub:
		delivery = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
		eventType = strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
		signature = strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	case ProviderGitea:
		delivery = strings.TrimSpace(r.Header.Get("X-Gitea-Delivery"))
		eventType = strings.TrimSpace(r.Header.Get("X-Gitea-Event"))
		signature = strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	default:
		return "", "", "", fmt.Errorf("unsupported webhook provider %q", h.provider)
	}
	if delivery == "" || eventType == "" {
		return "", "", "", fmt.Errorf("missing %s event headers", h.provider)
	}
	return delivery, eventType, signature, nil
}

func Verify(secret, body []byte, signature string) bool {
	const prefix = "sha256="
	if len(secret) == 0 || !strings.HasPrefix(signature, prefix) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func SignForTest(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}
