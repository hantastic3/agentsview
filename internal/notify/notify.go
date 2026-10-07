package notify

import (
	"context"
	"log"
	"math"
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

type sessionState struct {
	turnCount, replyCount int
	lastReply, endedAt    time.Time
}

type Hub struct {
	store   *db.DB
	cfg     func() config.NotificationsConfig
	publish func(Notification)
	now     func() time.Time
	since   time.Time
	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
	seen    map[string]sessionState
	retries map[string]time.Time
}

func New(store *db.DB, cfg func() config.NotificationsConfig, publish func(Notification)) *Hub {
	return &Hub{
		store: store, cfg: cfg, publish: publish, now: time.Now,
		since: time.Now(), pending: make(map[string]bool), wake: make(chan struct{}, 1),
		seen: make(map[string]sessionState), retries: make(map[string]time.Time),
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
	var timer *time.Timer
	var timerC <-chan time.Time
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
		case <-timerC:
		}
		if timer != nil {
			timer.Stop()
		}
		timerC = nil
		if next := h.process(ctx); !next.IsZero() {
			timer = time.NewTimer(next.Sub(h.now()))
			timerC = timer.C
		}
	}
}

func (h *Hub) process(ctx context.Context) time.Time {
	now := h.now()
	cfg := h.cfg()
	h.mu.Lock()
	ids := h.pending
	h.pending = make(map[string]bool)
	h.mu.Unlock()
	for id, state := range h.seen {
		if now.Sub(state.endedAt) > 10*time.Minute {
			delete(h.seen, id)
			delete(h.retries, id)
		}
	}
	if !cfg.Enabled {
		h.since = now
		clear(h.retries)
		return time.Time{}
	}
	for id, due := range h.retries {
		if !due.After(now) || !cfg.NotifyNewReply {
			ids[id] = true
		}
	}
	for id := range ids {
		delete(h.retries, id)
		session, err := h.store.GetSession(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("notification session read: %v", err)
			}
			continue
		}
		if session == nil || session.ParentSessionID != nil || len(session.ParentSessionIDs) > 0 || session.IsAutomated || session.EndedAt == nil {
			continue
		}
		endedAt, err := time.Parse(time.RFC3339Nano, *session.EndedAt)
		if err != nil || !endedAt.After(h.since) || now.Sub(endedAt) > 10*time.Minute {
			continue
		}
		state, exists := h.seen[id]
		if !exists {
			state.turnCount, state.replyCount = -1, -1
		}
		state.endedAt = endedAt
		turnEnd := session.TerminationStatus != nil && *session.TerminationStatus == "awaiting_user" && session.MessageCount != state.turnCount
		if !turnEnd && (!cfg.NotifyNewReply || session.MessageCount == state.replyCount || session.MessageCount == state.turnCount) {
			h.seen[id] = state
			continue
		}
		messages, err := h.store.GetMessages(ctx, id, math.MaxInt, 8, false)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("notification message read: %v", err)
			}
			continue
		}
		var latest *db.Message
		for i := range messages {
			if !messages[i].IsSystem {
				latest = &messages[i]
				break
			}
		}
		n := Notification{Kind: "turn_end", SessionID: id, Project: session.Project, Agent: session.Agent}
		if session.DisplayName != nil {
			n.DisplayName = *session.DisplayName
		}
		if latest != nil && latest.Role == "assistant" {
			n.Excerpt = stringutil.TruncateRunes(latest.Content, 160, "…")
		}
		if turnEnd {
			state.turnCount = session.MessageCount
		} else if latest == nil || latest.Role != "assistant" {
			h.seen[id] = state
			continue
		} else if due := state.lastReply.Add(time.Minute); due.After(now) {
			h.retries[id] = due
			h.seen[id] = state
			continue
		} else {
			n.Kind = "new_reply"
			state.replyCount, state.lastReply = session.MessageCount, now
		}
		h.seen[id] = state
		h.publish(n)
	}
	var next time.Time
	for _, due := range h.retries {
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next
}
