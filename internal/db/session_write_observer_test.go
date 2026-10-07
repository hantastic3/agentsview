package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionWriteObserverAfterCommit(t *testing.T) {
	d := testDB(t)
	var observed []string
	d.SetSessionWriteObserver(func(ids []string) {
		for _, id := range ids {
			messages, err := d.GetMessages(t.Context(), id, 0, 8, true)
			require.NoError(t, err)
			require.NotEmpty(t, messages, "committed rows visible on the reader")
			observed = append(observed, id)
		}
	})
	result, err := d.WriteSessionBatch([]SessionBatchWrite{{
		Session:  Session{ID: "session", Project: "demo", Agent: "claude", MessageCount: 1},
		Messages: []Message{{SessionID: "session", Ordinal: 0, Role: "user", Content: "hello"}},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, result.WrittenSessions)
	assert.Equal(t, []string{"session"}, observed)
	_, err = d.WriteSessionIncremental(t.Context(), "session",
		[]Message{{SessionID: "session", Ordinal: 1, Role: "assistant", Content: "done"}},
		IncrementalSessionUpdate{MsgCount: 1, NextOrdinal: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"session", "session"}, observed)
	messages, err := d.GetMessages(t.Context(), "session", 0, 8, true)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "done", messages[1].Content)
	_, err = d.WriteSessionIncremental(t.Context(), "missing",
		[]Message{{SessionID: "missing", Ordinal: 0, Role: "assistant", Content: "invalid parent"}},
		IncrementalSessionUpdate{MsgCount: 1, NextOrdinal: 1})
	require.Error(t, err)
	assert.Equal(t, []string{"session", "session"}, observed)
}
