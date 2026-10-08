package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestClaudeTurnDuration(t *testing.T) {
	const tsEarlyS2 = "2024-01-01T10:00:02Z"
	const duration = `{"type":"system","subtype":"turn_duration","durationMs":1000}` + "\n"
	const pending = `{"type":"system","subtype":"turn_duration","durationMs":1000,"pendingBackgroundAgentCount":2}` + "\n"
	answer := testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn") + "\n"
	initial := testjsonl.JoinJSONL(testjsonl.ClaudeUserJSON("hello", tsEarly), testjsonl.ClaudeAssistantJSON("ready", tsEarlyS1, "end_turn")) + duration + testjsonl.ClaudeUserJSON("continue", tsEarlyS2) + "\n"
	for _, tc := range []struct {
		name     string
		producer string
		initial  string
		tails    []string
		want     []TerminationStatus
	}{
		{
			name: "blocked then allowed stop", initial: initial,
			tails: []string{answer, testjsonl.ClaudeUserJSON("Stop hook feedback: keep working", tsEarlyS2) + "\n", `{"type":"system","subtype":"stop_hook_summary","hookErrors":["blocked"]}` + "\n", answer, `{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}` + "\n", duration},
			want:  []TerminationStatus{TerminationClean, TerminationClean, TerminationClean, TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "background agents", initial: initial,
			tails: []string{answer, pending, testjsonl.ClaudeUserJSON("<task-notification>agent finished</task-notification>", tsEarlyS2) + "\n", answer, duration},
			want:  []TerminationStatus{TerminationClean, TerminationClean, TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "duration in same tail", initial: initial,
			tails: []string{answer + duration}, want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "duration before assistant in same tail", initial: initial,
			tails: []string{duration + answer, duration}, want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "first interactive turn", initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer, duration}, want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "oldest verified cli", producer: `"entrypoint":"cli","version":"2.1.259",`, initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer, duration}, want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "duration finishes pending tool", initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-a","name":"Read","input":{}}],"stop_reason":"tool_use"}}` + "\n", duration},
			want:  []TerminationStatus{TerminationToolCallPending, TerminationAwaitingUser},
		},
		{
			name: "older cli", producer: `"entrypoint":"cli","version":"2.1.200",`, initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer}, want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "sdk-cli", producer: `"entrypoint":"sdk-cli","version":"2.1.266",`, initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer}, want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "no version", producer: `"entrypoint":"cli",`, initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer}, want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "sidechain", producer: `"entrypoint":"cli","version":"2.1.266","isSidechain":true,`, initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n",
			tails: []string{answer}, want: []TerminationStatus{TerminationAwaitingUser},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := tc.producer
			if producer == "" {
				producer = `"entrypoint":"cli","version":"2.1.266",`
			}
			tc.initial = claudeTurnDurationProducer(tc.initial, producer)
			root := t.TempDir()
			path := filepath.Join(root, "project", "session.jsonl")
			writeSourceFile(t, path, tc.initial)
			provider, ok := NewProvider(AgentClaude, ProviderConfig{Roots: []string{root}})
			require.True(t, ok)
			source, ok, err := provider.FindSource(t.Context(), FindSourceRequest{RawSessionID: "session"})
			require.NoError(t, err)
			require.True(t, ok)
			results, err := parseClaudeSession(path, "project", "local")
			require.NoError(t, err)
			require.Len(t, results, 1)
			ordinal := len(results[0].Messages)
			entrypoint := results[0].Session.Entrypoint
			content := tc.initial
			status := TerminationClean
			waiting := 0
			for i, tail := range tc.tails {
				tail = claudeTurnDurationProducer(tail, producer)
				offset := int64(len(content))
				content += tail
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				results, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, results, 1)
				assert.Equal(t, tc.want[i], results[0].Session.TerminationStatus, "full parse record %d", i)
				outcome, applied, err := provider.ParseIncremental(t.Context(), IncrementalRequest{
					Source: source, Fingerprint: SourceFingerprint{Key: path, Size: int64(len(content))},
					SessionID: "session", Offset: offset, StartOrdinal: ordinal, StoredEntrypoint: entrypoint,
				})
				require.NoError(t, err)
				require.Equal(t, IncrementalApplied, applied)
				if tc.tails[i] == duration || tc.tails[i] == pending {
					require.NotNil(t, outcome.TerminationStatus)
					assert.Empty(t, outcome.Messages)
				}
				assert.Equal(t, int64(len(tail)), outcome.ConsumedBytes)
				ordinal += len(outcome.Messages)
				assert.Len(t, results[0].Messages, ordinal, "duration records stay out of messages")
				if outcome.TerminationStatus != nil {
					status = *outcome.TerminationStatus
					if status == TerminationAwaitingUser {
						waiting++
					}
				}
				assert.Equal(t, tc.want[i], status, "incremental record %d", i)
			}
			assert.Equal(t, 1, waiting)
		})
	}
}

func TestClaudeTurnDurationPrecedence(t *testing.T) {
	const tsEarlyS2 = "2024-01-01T10:00:02Z"
	const duration = `{"type":"system","subtype":"turn_duration"}` + "\n"
	for _, tc := range []struct {
		name, tail string
		want       TerminationStatus
	}{
		{"truncated", testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn") + "\n" + duration + `{"type":"user"`, TerminationTruncated},
		{"user replied", testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn") + "\n" + duration + testjsonl.ClaudeUserJSON("more", tsEarlyS2) + "\n", TerminationClean},
		{"compact boundary", testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn") + "\n" + duration + `{"type":"assistant","uuid":"summary","isCompactSummary":true,"message":{"content":"summary"}}` + "\n", TerminationAwaitingUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n" + tc.tail
			content = claudeTurnDurationProducer(content, `"entrypoint":"cli","version":"2.1.266",`)
			session, _ := runClaudeParserTest(t, "session.jsonl", content)
			assert.Equal(t, tc.want, session.TerminationStatus)
		})
	}
}

func claudeTurnDurationProducer(content, producer string) string {
	return "{" + producer + strings.ReplaceAll(content[1:], "\n{", "\n{"+producer)
}
