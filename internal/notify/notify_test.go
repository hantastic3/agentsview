package notify

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"testing/synctest"
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
			_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{Session: session, ReplaceMessages: true, Messages: []db.Message{{SessionID: "session", Ordinal: 0, Role: "assistant", Content: "done", Timestamp: now.Add(-tc.age).Format(time.RFC3339Nano)}}}})
			require.NoError(t, err)
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
	_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
		Session: db.Session{ID: "session", Agent: "claude", Project: "demo", MessageCount: 1, TerminationStatus: dbtest.Ptr("awaiting_user")}, ReplaceMessages: true,
		Messages: []db.Message{{SessionID: "session", Ordinal: 0, Role: "assistant", Content: "done", Timestamp: now.Format(time.RFC3339Nano)}},
	}})
	require.NoError(t, err)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	cfg.Enabled = true
	now = now.Add(time.Second)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Empty(t, sent)
}

func TestHubAnswerIdentity(t *testing.T) {
	for _, kind := range []string{"turn_end", "new_reply"} {
		t.Run(kind, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			ended := now.Format(time.RFC3339Nano)
			answerAt := ended
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
					{SessionID: "session", Ordinal: 1, Role: "assistant", Content: content, Timestamp: answerAt},
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
			now = now.Add(time.Second)
			ended = now.Format(time.RFC3339Nano)
			write("first answer", 3)
			assert.Len(t, sent, 1, "a system-only append moves ended_at without publishing again")
			now = now.Add(11 * time.Minute)
			ended = now.Format(time.RFC3339Nano)
			write("first answer", 3)
			assert.Len(t, sent, 1, "progress after the freshness window stays silent")
			answerAt = ended
			write("retry answer", 2)
			require.Len(t, sent, 2)
			assert.Equal(t, "retry answer", sent[1].Excerpt)
			now = now.Add(time.Minute)
			ended = now.Format(time.RFC3339Nano)
			write("image stripped", 3)
			assert.Len(t, sent, 2, "rewrites keep the answer timestamp even when the count changes")
			now = now.Add(time.Minute)
			ended = now.Format(time.RFC3339Nano)
			answerAt = ended
			write("new answer", 3)
			require.Len(t, sent, 3, "a replaced answer has a new identity even with the same ordinal and count")
			assert.Equal(t, "new answer", sent[2].Excerpt)
		})
	}
}

func TestHubNativeIdentitySurvivesReplacement(t *testing.T) {
	for _, kind := range []string{"turn_end", "new_reply"} {
		t.Run(kind, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig {
				return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
			}, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now.Add(-time.Second)
			session := db.Session{ID: "session", Agent: "claude", MessageCount: 2}
			if kind == "turn_end" {
				session.TerminationStatus = dbtest.Ptr("awaiting_user")
			}
			answer := db.Message{SessionID: "session", Ordinal: 1, Role: "assistant", Content: "done", Timestamp: now.Format(time.RFC3339Nano), SourceUUID: "answer-id"}
			for _, moved := range []bool{false, true} {
				messages := []db.Message{{SessionID: "session", Role: "user", Content: "question"}, answer}
				if moved {
					messages = []db.Message{answer}
					messages[0].Ordinal = 0
					session.MessageCount = 1
				}
				_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{Session: session, ReplaceMessages: true, Messages: messages}})
				require.NoError(t, err)
				hub.Enqueue([]string{"session"})
				hub.process(t.Context())
				require.Len(t, sent, 1, "moving the same native answer stays silent")
				assert.Equal(t, kind, sent[0].Kind)
			}
		})
	}
}

func TestHubReadErrorRetriesAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := New(nil, func() config.NotificationsConfig { return config.NotificationsConfig{Enabled: true} }, func(Notification) { t.Error("unexpected notification") })
		reader := &failingReadStore{failSession: true}
		hub.store = reader
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go hub.Run(ctx)
		hub.Enqueue([]string{"session"})
		synctest.Wait()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		assert.Equal(t, 3, reader.sessionReads)
	})
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
		messages := []db.Message{{SessionID: "session", Ordinal: 0, Role: role, Content: "latest conversation message", Timestamp: now.Format(time.RFC3339Nano)}}
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

func TestHubAssistantRows(t *testing.T) {
	for _, tc := range []struct {
		name, role, content string
		termination         string
		subtype             string
		toolUse             bool
		want                int
		wantKind            string
	}{
		{name: "tool call reply", role: "assistant", content: "running", toolUse: true, termination: "tool_call_pending"},
		{name: "resolved tool turn", role: "assistant", content: "running", toolUse: true, want: 1, wantKind: "turn_end"},
		{name: "resolved tool without prose", role: "assistant", toolUse: true, want: 1, wantKind: "turn_end"},
		{name: "tool result reply", role: "assistant", content: "command output", subtype: "tool_result", termination: "clean"},
		{name: "text reply", role: "assistant", content: "answer", termination: "clean", want: 1, wantKind: "new_reply"},
		{name: "empty reply", role: "assistant", termination: "tool_call_pending"},
		{name: "empty turn end", role: "assistant", want: 1, wantKind: "turn_end"},
		{name: "prompt after answer", role: "user", content: "follow up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig {
				return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
			}, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now.Add(-time.Second)
			termination := "awaiting_user"
			if tc.termination != "" {
				termination = tc.termination
			}
			_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
				Session:         db.Session{ID: "session", Agent: "claude", Project: "demo", MessageCount: 1, TerminationStatus: &termination},
				ReplaceMessages: true,
				Messages:        []db.Message{{SessionID: "session", Ordinal: 0, Role: tc.role, Content: tc.content, HasToolUse: tc.toolUse, SourceSubtype: tc.subtype, Timestamp: now.Format(time.RFC3339Nano)}},
			}})
			require.NoError(t, err)
			hub.Enqueue([]string{"session"})
			hub.process(t.Context())
			require.Len(t, sent, tc.want)
			if tc.want > 0 {
				assert.Equal(t, tc.wantKind, sent[0].Kind)
				assert.Equal(t, tc.content, sent[0].Excerpt)
			}
			hub.Enqueue([]string{"session"})
			hub.process(t.Context())
			assert.Len(t, sent, tc.want, "same message fires once")
		})
	}
}

type failingReadStore struct {
	*db.DB
	failSession  bool
	failMessage  bool
	sessionReads int
}

func (s *failingReadStore) GetSession(ctx context.Context, id string) (*db.Session, error) {
	s.sessionReads++
	if s.failSession {
		return nil, errors.New("session read unavailable")
	}
	return s.DB.GetSession(ctx, id)
}

func (s *failingReadStore) GetLatestNonSystemMessage(ctx context.Context, id string) (*db.Message, error) {
	if s.failMessage {
		return nil, errors.New("message read unavailable")
	}
	return s.DB.GetLatestNonSystemMessage(ctx, id)
}

func TestHubReadErrorRetriesOnTimer(t *testing.T) {
	for _, read := range []string{"session", "message"} {
		t.Run(read, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := dbtest.OpenTestDB(t)
				now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
				_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
					Session: db.Session{ID: "session", Agent: "codex", MessageCount: 1, TerminationStatus: dbtest.Ptr("awaiting_user")}, ReplaceMessages: true,
					Messages: []db.Message{{SessionID: "session", Role: "assistant", Content: "done", Timestamp: now.Format(time.RFC3339Nano)}},
				}})
				require.NoError(t, err)
				var sent []Notification
				hub := New(store, func() config.NotificationsConfig { return config.NotificationsConfig{Enabled: true} }, func(n Notification) { sent = append(sent, n) })
				hub.now = func() time.Time { return now }
				hub.since = now.Add(-time.Second)
				reader := &failingReadStore{DB: store, failSession: read == "session", failMessage: read == "message"}
				hub.store = reader
				var logs bytes.Buffer
				oldOutput := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(oldOutput) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				go hub.Run(ctx)
				hub.Enqueue([]string{"session"})
				synctest.Wait()
				assert.Empty(t, sent)
				time.Sleep(3 * time.Second)
				synctest.Wait()
				assert.Empty(t, sent)
				assert.Equal(t, 1, strings.Count(logs.String(), "notification read:"))
				reader.failSession, reader.failMessage = false, false
				time.Sleep(3 * time.Second)
				synctest.Wait()
				require.Len(t, sent, 1)
				assert.Equal(t, "session", sent[0].SessionID)
				assert.Equal(t, "turn_end", sent[0].Kind)
				time.Sleep(9 * time.Second)
				synctest.Wait()
				assert.Len(t, sent, 1)
			})
		})
	}
}

func TestHubCodexReplyThenTaskComplete(t *testing.T) {
	store := dbtest.OpenTestDB(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var sent []Notification
	hub := New(store, func() config.NotificationsConfig {
		return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
	}, func(n Notification) { sent = append(sent, n) })
	hub.now = func() time.Time { return now }
	hub.since = now.Add(-time.Second)
	_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
		Session: db.Session{ID: "session", Agent: "codex", Project: "demo", MessageCount: 1}, ReplaceMessages: true,
		Messages: []db.Message{{SessionID: "session", Ordinal: 0, Role: "assistant", Content: strings.Repeat("界", 161), Timestamp: now.Format(time.RFC3339Nano)}},
	}})
	require.NoError(t, err)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	require.Len(t, sent, 1)
	assert.Equal(t, "new_reply", sent[0].Kind)
	assert.Equal(t, strings.Repeat("界", 160)+"…", sent[0].Excerpt)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Len(t, sent, 1, "same reply fires once")
	require.NoError(t, store.UpdateSessionIncremental(t.Context(), "session", db.IncrementalSessionUpdate{MsgCount: 1, TerminationStatus: dbtest.Ptr("awaiting_user")}))
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	require.Len(t, sent, 2)
	assert.Equal(t, "turn_end", sent[1].Kind)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Len(t, sent, 2, "same completion fires once")
	require.NoError(t, store.UpdateSessionIncremental(t.Context(), "session", db.IncrementalSessionUpdate{MsgCount: 1, TerminationStatus: dbtest.Ptr("tool_call_pending")}))
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Len(t, sent, 2, "a status rewrite cannot repeat the reply")
}

func TestHubUsageOnlyTurnEnd(t *testing.T) {
	store := dbtest.OpenTestDB(t)
	store.SetArchiveContent(config.ArchiveContentUsage)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var sent []Notification
	hub := New(store, func() config.NotificationsConfig {
		return config.NotificationsConfig{Enabled: true, NotifyNewReply: true}
	}, func(n Notification) { sent = append(sent, n) })
	hub.now = func() time.Time { return now }
	hub.since = now.Add(-time.Second)
	_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
		Session: db.Session{ID: "session", Agent: "codex", Project: "demo", MessageCount: 1}, ReplaceMessages: true,
		Messages: []db.Message{{SessionID: "session", Ordinal: 0, Role: "assistant", Content: "done", Timestamp: now.Format(time.RFC3339Nano)}},
	}})
	require.NoError(t, err)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Empty(t, sent, "usage-only replies have no text to toast")
	require.NoError(t, store.UpdateSessionIncremental(t.Context(), "session", db.IncrementalSessionUpdate{MsgCount: 1, TerminationStatus: dbtest.Ptr("awaiting_user")}))
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	require.Len(t, sent, 1)
	assert.Equal(t, "turn_end", sent[0].Kind)
	assert.Empty(t, sent[0].Excerpt)
	hub.Enqueue([]string{"session"})
	hub.process(t.Context())
	assert.Len(t, sent, 1)
}

func TestHubTimestampFallback(t *testing.T) {
	for _, tc := range []struct {
		name, timestamp string
		endedAt         *string
		want            int
	}{
		{name: "missing row timestamp", endedAt: dbtest.Ptr("2026-01-01T12:00:00Z"), want: 1},
		{name: "invalid row timestamp", timestamp: "invalid", endedAt: dbtest.Ptr("2026-01-01T12:00:00Z"), want: 1},
		{name: "stale fallback", endedAt: dbtest.Ptr("2026-01-01T11:49:00Z")},
		{name: "before hub started", endedAt: dbtest.Ptr("2026-01-01T11:59:58Z")},
		{name: "missing fallback"},
		{name: "invalid fallback", endedAt: dbtest.Ptr("invalid")},
		{name: "valid stale row wins", timestamp: "2026-01-01T11:49:00Z", endedAt: dbtest.Ptr("2026-01-01T12:00:00Z")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbtest.OpenTestDB(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			var sent []Notification
			hub := New(store, func() config.NotificationsConfig {
				return config.NotificationsConfig{Enabled: true}
			}, func(n Notification) { sent = append(sent, n) })
			hub.now = func() time.Time { return now }
			hub.since = now.Add(-time.Second)
			_, err := store.WriteSessionBatch([]db.SessionBatchWrite{{
				Session: db.Session{ID: "session", Agent: "claude", Project: "demo", MessageCount: 1, EndedAt: tc.endedAt, TerminationStatus: dbtest.Ptr("awaiting_user")}, ReplaceMessages: true,
				Messages: []db.Message{{SessionID: "session", Ordinal: 0, Role: "assistant", Content: "done", Timestamp: tc.timestamp}},
			}})
			require.NoError(t, err)
			hub.Enqueue([]string{"session"})
			hub.process(t.Context())
			require.Len(t, sent, tc.want)
			if tc.want > 0 {
				assert.Equal(t, "turn_end", sent[0].Kind)
				assert.Equal(t, "done", sent[0].Excerpt)
				now = now.Add(time.Second)
				require.NoError(t, store.UpdateSessionIncremental(t.Context(), "session", db.IncrementalSessionUpdate{MsgCount: 1, EndedAt: dbtest.Ptr(now.Format(time.RFC3339Nano))}))
				hub.Enqueue([]string{"session"})
				hub.process(t.Context())
				assert.Len(t, sent, 1, "fallback time changes preserve message identity")
			}
		})
	}
}
