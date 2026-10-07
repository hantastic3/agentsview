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
	ordinal   int
	timestamp string
}

type sessionStore interface {
	GetSession(context.Context, string) (*db.Session, error)
	GetLatestNonSystemMessage(context.Context, string) (*db.Message, error)
}

type Hub struct {
	store   sessionStore
	cfg     func() config.NotificationsConfig
	publish func(Notification)
	now     func() time.Time
	since   time.Time
	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
	seen    map[string]messageIdentity
}

func New(store sessionStore, cfg func() config.NotificationsConfig, publish func(Notification)) *Hub {
	return &Hub{
		store: store, cfg: cfg, publish: publish, now: time.Now,
		since: time.Now(), pending: make(map[string]bool), wake: make(chan struct{}, 1),
		seen: make(map[string]messageIdentity),
	}
}

// Enqueue only copies IDs because the archive writer holds its mutex.
func (h *Hub) Enqueue(ids []string) {
	h.mu.Lock()
	for _, id := range ids {
		h.pending[id] = true
	}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
			h.process(ctx)
		}
	}
}

func (h *Hub) process(ctx context.Context) {
	now := h.now()
	cfg := h.cfg()
	h.mu.Lock()
	ids := h.pending
	h.pending = make(map[string]bool)
	h.mu.Unlock()
	for id, identity := range h.seen {
		timestamp, err := time.Parse(time.RFC3339Nano, identity.timestamp)
		if err != nil || now.Sub(timestamp) > 10*time.Minute {
			delete(h.seen, id)
		}
	}
	if !cfg.Enabled {
		h.since = now
		return
	}
	for id := range ids {
		session, err := h.store.GetSession(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("notification session read: %v", err)
			}
			continue
		}
		if session == nil || session.RelationshipType == "subagent" || session.IsAutomated {
			continue
		}
		latest, err := h.store.GetLatestNonSystemMessage(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("notification message read: %v", err)
			}
			continue
		}
		if latest == nil || latest.Role != "assistant" || latest.HasToolUse || latest.Content == "" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, latest.Timestamp)
		if err != nil || !timestamp.After(h.since) || now.Sub(timestamp) > 10*time.Minute {
			continue
		}
		identity := messageIdentity{ordinal: latest.Ordinal, timestamp: latest.Timestamp}
		if seen, ok := h.seen[id]; ok && seen == identity {
			continue
		}
		n := Notification{Kind: "turn_end", SessionID: id, Project: session.Project, Agent: session.Agent, Excerpt: stringutil.TruncateRunes(latest.Content, 160, "…")}
		if session.TerminationStatus == nil || *session.TerminationStatus != "awaiting_user" {
			if !cfg.NotifyNewReply {
				continue
			}
			n.Kind = "new_reply"
		}
		if session.DisplayName != nil {
			n.DisplayName = *session.DisplayName
		}
		h.seen[id] = identity
		h.publish(n)
	}
}
