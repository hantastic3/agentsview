package notify

import (
	"context"
	"log"
	"sync"
	"time"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/stringutil"
)

type Notification struct {
	Kind        string `json:"kind"`
	SessionID   string `json:"session_id"`
	Project     string `json:"project"`
	Agent       string `json:"agent"`
	DisplayName string `json:"display_name"`
	Excerpt     string `json:"excerpt"`
}

type messageIdentity struct {
	sourceUUID string
	ordinal    int
	timestamp  string
}

const FreshnessWindow = 10 * time.Minute

type notifiedMessage struct {
	messageIdentity
	at       time.Time
	turnEnd  bool
	newReply bool
}

type Hub struct {
	store interface {
		GetSession(context.Context, string) (*db.Session, error)
		GetLatestNonSystemMessage(context.Context, string) (*db.Message, error)
	}
	cfg     func() config.NotificationsConfig
	publish func(Notification)
	now     func() time.Time
	since   time.Time
	mu      sync.Mutex
	pending map[string]int // Failed read attempts.
	wake    chan struct{}
	seen    map[string]notifiedMessage
}

func New(store *db.DB, cfg func() config.NotificationsConfig, publish func(Notification)) *Hub {
	return &Hub{
		store: store, cfg: cfg, publish: publish, now: time.Now,
		since: time.Now(), pending: make(map[string]int), wake: make(chan struct{}, 1),
		seen: make(map[string]notifiedMessage),
	}
}

// Enqueue only copies IDs because the archive writer holds its mutex.
func (h *Hub) Enqueue(ids []string) {
	h.mu.Lock()
	for _, id := range ids {
		if _, ok := h.pending[id]; !ok {
			h.pending[id] = 0
		}
	}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *Hub) Run(ctx context.Context) {
	var retry <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-retry:
			retry = nil
		}
		h.process(ctx)
		h.mu.Lock()
		hasPending := len(h.pending) > 0
		h.mu.Unlock()
		if hasPending && retry == nil {
			timer = time.NewTimer(3 * time.Second)
			retry = timer.C
		}
	}
}

func (h *Hub) process(ctx context.Context) {
	now := h.now()
	cfg := h.cfg()
	h.mu.Lock()
	ids := h.pending
	h.pending = make(map[string]int)
	h.mu.Unlock()
	for id, identity := range h.seen {
		if now.Sub(identity.at) > FreshnessWindow {
			delete(h.seen, id)
		}
	}
	if !cfg.Enabled {
		h.since = now
		return
	}
	for id, attempts := range ids {
		session, err := h.store.GetSession(ctx, id)
		if err != nil {
			h.retryRead(ctx, id, attempts, err)
			continue
		}
		if session == nil || session.RelationshipType == "subagent" || session.IsAutomated {
			continue
		}
		latest, err := h.store.GetLatestNonSystemMessage(ctx, id)
		if err != nil {
			h.retryRead(ctx, id, attempts, err)
			continue
		}
		if latest == nil || latest.Role != "assistant" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, latest.Timestamp)
		if err != nil && session.EndedAt != nil {
			timestamp, err = time.Parse(time.RFC3339Nano, *session.EndedAt)
		}
		if err != nil || !timestamp.After(h.since) || now.Sub(timestamp) > FreshnessWindow {
			continue
		}
		identity := messageIdentity{ordinal: latest.Ordinal, timestamp: latest.Timestamp}
		if latest.SourceUUID != "" {
			identity = messageIdentity{sourceUUID: latest.SourceUUID}
		}
		seen, ok := h.seen[id]
		if !ok || seen.messageIdentity != identity {
			seen = notifiedMessage{messageIdentity: identity, at: timestamp}
		}
		n := Notification{Kind: "turn_end", SessionID: id, Project: session.Project, Agent: session.Agent, Excerpt: stringutil.TruncateRunes(latest.Content, 160, "…")}
		if session.TerminationStatus == nil || *session.TerminationStatus != "awaiting_user" {
			if !cfg.NotifyNewReply || latest.HasToolUse || latest.SourceSubtype == "tool_result" || latest.Content == "" || seen.newReply {
				continue
			}
			n.Kind = "new_reply"
			seen.newReply = true
		} else {
			if seen.turnEnd {
				continue
			}
			seen.turnEnd = true
		}
		if session.DisplayName != nil {
			n.DisplayName = *session.DisplayName
		}
		h.seen[id] = seen
		h.publish(n)
	}
}

func (h *Hub) retryRead(ctx context.Context, id string, attempts int, err error) {
	if ctx.Err() != nil {
		return
	}
	if attempts == 0 {
		log.Printf("notification read: %v", err)
	}
	if attempts < 2 {
		h.mu.Lock()
		h.pending[id] = attempts + 1
		h.mu.Unlock()
	}
}
