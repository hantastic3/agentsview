package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
)

func TestHubNotifications(t *testing.T) {
	for _, tc := range []struct {
		name         string
		age          time.Duration
		relationship string
		automated    bool
		want         int
	}{
		{name: "history rewritten", age: time.Hour},
		{name: "fresh turn", age: -time.Second, want: 1},
		{name: "subagent", age: -time.Second, relationship: "subagent"},
		{name: "fork", age: -time.Second, relationship: "fork", want: 1},
		{name: "continuation", age: -time.Second, relationship: "continuation", want: 1},
		{name: "automated", age: -time.Second, automated: true},
		{name: "outside freshness window", age: 11 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig {
				return config.NotificationsConfig{Enabled: true}
			}, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now
			session := db.Session{
				ID: "session", Agent: "claude", Project: "demo", MessageCount: 2,
				EndedAt:           dbtest.Ptr(now.Add(-tc.age).Format(time.RFC3339Nano)),
				TerminationStatus: dbtest.Ptr("awaiting_user"), IsAutomated: tc.automated,
			}
			if tc.relationship != "" {
				require.NoError(t, store.UpsertSession(t.Context(), db.Session{ID: "parent", Agent: "claude", Project: "demo"}))
				session.ParentSessionID = dbtest.Ptr("parent")
				session.RelationshipType = tc.relationship
			}
			require.NoError(t, store.UpsertSession(t.Context(), session))
			hub.Enqueue([]string{"session"})
			hub.process(t.Context())
			assert.Len(t, sent, tc.want)
			if tc.want > 0 {
				assert.Equal(t, "turn_end", sent[0].Kind)
				assert.Equal(t, "session", sent[0].SessionID)
			}
			hub.Enqueue([]string{"session"})
			hub.process(t.Context())
			assert.Len(t, sent, tc.want, "same turn fires once")
		})
	}
}

func TestHubDisabledTurnStaysSilent(t *testing.T) {
	store := dbtest.OpenTestDB(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := config.NotificationsConfig{}
	var sent []Notification
	hub := New(store, func() config.NotificationsConfig { return cfg }, func(n Notification) { sent = append(sent, n) })
	hub.now = func() time.Time { return now }
	hub.since = now.Add(-time.Minute)
	require.NoError(t, store.UpsertSession(t.Context(), db.Session{
		ID: "session", Agent: "claude", Project: "demo",
		MessageCount: 1, EndedAt: dbtest.Ptr(now.Format(time.RFC3339)), TerminationStatus: dbtest.Ptr("awaiting_user"),
	}))
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	cfg.Enabled = true
	now = now.Add(time.Second)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Empty(t, sent)
}

func TestHubReplyThrottleAndTurnEnd(t *testing.T) {
	store := dbtest.OpenTestDB(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var sent []Notification
	hub := New(store, func() config.NotificationsConfig {
		return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
	}, func(n Notification) { sent = append(sent, n) })
	hub.now = func() time.Time { return now }
	hub.since = now.Add(-time.Second)
	write := func(content string, termination string) {
		t.Helper()
		_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
			Session: db.Session{
				ID: "session", Agent: "codex", Project: "demo", MessageCount: 2,
				EndedAt: dbtest.Ptr(now.Format(time.RFC3339Nano)), TerminationStatus: dbtest.Ptr(termination),
			},
			ReplaceMessages: true,
			Messages: []db.Message{
				{SessionID: "session", Ordinal: 0, Role: "assistant", Content: content},
				{SessionID: "session", Ordinal: 1, Role: "user", Content: "system notice", IsSystem: true},
			},
		}})
		require.NoError(t, err)
		hub.Enqueue([]string{"session"})
	}
	write(strings.Repeat("界", 161), "tool_call_pending")
	hub.process(t.Context())
	require.Len(t, sent, 1)
	assert.Equal(t, "new_reply", sent[0].Kind)
	assert.Equal(t, strings.Repeat("界", 160)+"…", sent[0].Excerpt)
	now = now.Add(10 * time.Second)
	write("second reply", "tool_call_pending")
	assert.Equal(t, now.Add(50*time.Second), hub.process(t.Context()))
	assert.Len(t, sent, 1)
	now = now.Add(20 * time.Second)
	write("latest reply", "tool_call_pending")
	hub.process(t.Context())
	assert.Len(t, sent, 1)
	now = now.Add(30 * time.Second)
	hub.process(t.Context())
	require.Len(t, sent, 2)
	assert.Equal(t, "latest reply", sent[1].Excerpt)
	write("latest reply", "awaiting_user")
	hub.process(t.Context())
	require.Len(t, sent, 3)
	assert.Equal(t, "turn_end", sent[2].Kind)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Len(t, sent, 3)
}

func TestHubAnswerIdentity(t *testing.T) {
	for _, kind := range []string{"turn_end", "new_reply"} {
		t.Run(kind, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			ended := now.Format(time.RFC3339Nano)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig {
				return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
			}, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now.Add(-time.Second)
			termination := "tool_call_pending"
			if kind == "turn_end" {
				termination = "awaiting_user"
			}
			write := func(content string, count int) {
				t.Helper()
				messages := []db.Message{
					{SessionID: "session", Ordinal: 0, Role: "user", Content: "question"},
					{SessionID: "session", Ordinal: 1, Role: "assistant", Content: content},
				}
				if count == 3 {
					messages = append(messages, db.Message{SessionID: "session", Ordinal: 2, Role: "user", IsSystem: true, Content: "system notice"})
				}
				_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
					Session:         db.Session{ID: "session", Agent: "claude", Project: "demo", MessageCount: count, EndedAt: &ended, TerminationStatus: &termination},
					ReplaceMessages: true,
					Messages:        messages,
				}})
				require.NoError(t, err)
				hub.Enqueue([]string{"session"})
				hub.process(t.Context())
			}
			write("first answer", 2)
			require.Len(t, sent, 1)
			assert.Equal(t, kind, sent[0].Kind)
			now = now.Add(time.Minute)
			ended = now.Format(time.RFC3339Nano)
			write("retry answer", 2)
			require.Len(t, sent, 2)
			assert.Equal(t, "retry answer", sent[1].Excerpt)
			write("image stripped", 3)
			assert.Len(t, sent, 2, "rewrites keep the answer timestamp even when the count changes")
		})
	}
}

func TestHubReplyBehindSystemRows(t *testing.T) {
	store := dbtest.OpenTestDB(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var sent []Notification
	hub := New(store, func() config.NotificationsConfig {
		return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
	}, func(n Notification) { sent = append(sent, n) })
	hub.now = func() time.Time { return now }
	hub.since = now.Add(-time.Second)
	for _, role := range []string{"assistant", "user"} {
		messages := []db.Message{{SessionID: "session", Ordinal: 0, Role: role, Content: "latest conversation message"}}
		for i := 1; i <= 12; i++ {
			messages = append(messages, db.Message{SessionID: "session", Ordinal: i, Role: "user", IsSystem: true, Content: "system notice"})
		}
		_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
			Session:         db.Session{ID: "session", Agent: "claude", Project: "demo", MessageCount: len(messages), EndedAt: dbtest.Ptr(now.Format(time.RFC3339Nano))},
			ReplaceMessages: true, Messages: messages,
		}})
		require.NoError(t, err)
		hub.Enqueue([]string{"session"})
		hub.process(t.Context())
		require.Len(t, sent, 1)
		assert.Equal(t, "new_reply", sent[0].Kind)
		assert.Equal(t, "latest conversation message", sent[0].Excerpt)
		now = now.Add(time.Minute)
	}
}

type failingSessionStore struct {
	*db.DB
	sessionError, messageError bool
}

func (s *failingSessionStore) GetSession(ctx context.Context, id string) (*db.Session, error) {
	if s.sessionError {
		return nil, errors.New("temporary session read failure")
	}
	return s.DB.GetSession(ctx, id)
}

func (s *failingSessionStore) GetLatestNonSystemMessage(ctx context.Context, id string) (*db.Message, error) {
	if s.messageError {
		return nil, errors.New("temporary message read failure")
	}
	return s.DB.GetLatestNonSystemMessage(ctx, id)
}

func TestHubReadErrorRetries(t *testing.T) {
	for _, failure := range []string{"session", "message"} {
		t.Run(failure, func(t *testing.T) {
			store := &failingSessionStore{DB: dbtest.OpenTestDB(t), sessionError: failure == "session", messageError: failure == "message"}
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig { return config.NotificationsConfig{Enabled: true} }, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now.Add(-time.Second)
			require.NoError(t, store.UpsertSession(t.Context(), db.Session{ID: "session", Agent: "claude", Project: "demo", EndedAt: dbtest.Ptr(now.Format(time.RFC3339Nano)), TerminationStatus: dbtest.Ptr("awaiting_user")}))
			hub.Enqueue([]string{"session"})
			assert.Equal(t, now.Add(time.Second), hub.process(t.Context()))
			assert.Empty(t, sent)
			hub.process(t.Context())
			assert.Empty(t, sent)
			store.sessionError, store.messageError = false, false
			now = now.Add(time.Second)
			hub.process(t.Context())
			require.Len(t, sent, 1)
			assert.Equal(t, "turn_end", sent[0].Kind)
			hub.process(t.Context())
			assert.Len(t, sent, 1)
		})
	}
}
