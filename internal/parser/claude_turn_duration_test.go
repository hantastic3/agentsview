package parser

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestClaudeTurnDuration(t *testing.T) {
	// Claude Code omits pendingBackgroundAgentCount and pendingWorkflowCount at zero.
	const tsEarlyS2 = "2024-01-01T10:00:02Z"
	const duration = `{"type":"system","subtype":"turn_duration","durationMs":1000}` + "\n"
	const pending = `{"type":"system","subtype":"turn_duration","durationMs":1000,"pendingBackgroundAgentCount":2}` + "\n"
	answer := testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn") + "\n"
	initial := testjsonl.JoinJSONL(testjsonl.ClaudeUserJSON("hello", tsEarly), testjsonl.ClaudeAssistantJSON("ready", tsEarlyS1, "end_turn")) + duration + testjsonl.ClaudeUserJSON("continue", tsEarlyS2) + "\n"
	for _, tc := range []struct {
		name        string
		producer    string
		initial     string
		tails       []string
		want        []TerminationStatus
		fullParseAt int
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
			name: "deferred swarm duration after new prompt", producer: `"entrypoint":"cli","version":"2.1.293",`,
			initial: `{"type":"user","uuid":"u0","message":{"content":"hello"}}` + "\n" + `{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}` + "\n",
			tails: []string{
				`{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"continue"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d0","parentUuid":"u1"}` + "\n",
				`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"done","stop_reason":"end_turn"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1"}` + "\n",
			},
			want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "deferred duration after stored prompt", fullParseAt: 1, producer: `"entrypoint":"cli","version":"2.1.293",`,
			initial: initial,
			tails:   []string{duration, answer + duration},
			want:    []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name:    "duration finishes stored tool result",
			initial: testjsonl.ClaudeUserJSON("hello", tsEarly) + "\n" + `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-a","name":"Read","input":{}}],"stop_reason":"tool_use"}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-a","content":"file contents"}]}}` + "\n",
			tails:   []string{duration}, want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "mixed tail duration belongs to older turn", fullParseAt: 1,
			initial: `{"type":"user","uuid":"u0","message":{"content":"hello"}}` + "\n" + `{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}` + "\n",
			tails: []string{
				`{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"continue"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d0","parentUuid":"a0"}` + "\n",
				`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"done","stop_reason":"end_turn"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1"}` + "\n",
			},
			want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "mixed tail resolves stop hook ancestry", initial: initial,
			tails: []string{
				`{"type":"assistant","uuid":"a1","message":{"content":"done","stop_reason":"end_turn"}}` + "\n" + `{"type":"system","subtype":"stop_hook_summary","uuid":"s1","parentUuid":"a1"}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"s1"}` + "\n",
			},
			want: []TerminationStatus{TerminationAwaitingUser},
		},
		{
			name: "mixed tail unresolved system ancestry", fullParseAt: 1,
			initial: `{"type":"user","uuid":"u0","message":{"content":"hello"}}` + "\n" + `{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}` + "\n" + `{"type":"system","subtype":"stop_hook_summary","uuid":"s0","parentUuid":"a0"}` + "\n",
			tails: []string{
				`{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"continue"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d0","parentUuid":"s0"}` + "\n",
				`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"done","stop_reason":"end_turn"}}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1"}` + "\n",
			},
			want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "duration belongs to an older turn", fullParseAt: 1,
			initial: `{"type":"user","uuid":"u0","message":{"content":"hello"}}` + "\n" + `{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}` + "\n" + `{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"continue"}}` + "\n",
			tails: []string{
				`{"type":"system","subtype":"turn_duration","uuid":"d0","parentUuid":"a0"}` + "\n",
				`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"done","stop_reason":"end_turn"}}` + "\n",
				`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1"}` + "\n",
			},
			want: []TerminationStatus{TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "malformed pending counts", initial: initial,
			tails: []string{
				answer + `{"type":"system","subtype":"turn_duration","pendingWorkflowCount":"unknown"}` + "\n",
				`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":0.5}` + "\n",
				`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":-1}` + "\n",
				`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":null}` + "\n",
				`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":false}` + "\n",
				duration,
			},
			want: []TerminationStatus{TerminationClean, TerminationClean, TerminationClean, TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "pending workflow", producer: `"entrypoint":"cli","version":"2.1.293",`, initial: initial,
			tails: []string{answer + `{"type":"system","subtype":"turn_duration","pendingWorkflowCount":1}` + "\n", `{"type":"system","subtype":"turn_duration","pendingWorkflowCount":1}` + "\n", duration},
			want:  []TerminationStatus{TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "duration descends through stop hook summary", fullParseAt: 3,
			initial: `{"type":"user","uuid":"u0","parentUuid":null,"message":{"content":"hello"}}` + "\n",
			tails: []string{
				`{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"done","stop_reason":"end_turn"}}` + "\n",
				`{"type":"system","subtype":"stop_hook_summary","uuid":"s0","parentUuid":"a0","hookErrors":[]}` + "\n",
				`{"type":"system","subtype":"turn_duration","uuid":"d0","parentUuid":"s0"}` + "\n",
			},
			want: []TerminationStatus{TerminationClean, TerminationClean, TerminationAwaitingUser},
		},
		{
			name: "duration before assistant in same tail", initial: initial,
			tails: []string{duration + answer, duration}, want: []TerminationStatus{TerminationClean, TerminationAwaitingUser},
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
			tc.initial = testjsonl.ClaudeProducerJSONL(tc.initial, producer)
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
			lastEntryUUID := results[0].Messages[ordinal-1].SourceUUID
			content := tc.initial
			status := TerminationClean
			waiting := 0
			for i, tail := range tc.tails {
				tail = testjsonl.ClaudeProducerJSONL(tail, producer)
				offset := int64(len(content))
				content += tail
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				results, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, results, 1)
				assert.Equal(t, tc.want[i], results[0].Session.TerminationStatus, "full parse record %d", i)
				outcome, applied, err := provider.ParseIncremental(t.Context(), IncrementalRequest{
					Source: source, Fingerprint: SourceFingerprint{Key: path, Size: int64(len(content))},
					SessionID: "session", Offset: offset, StartOrdinal: ordinal, StoredEntrypoint: entrypoint, LastEntryUUID: lastEntryUUID,
				})
				require.NoError(t, err)
				if tc.fullParseAt == i+1 {
					require.Equal(t, IncrementalNeedsFullParse, applied)
					require.True(t, outcome.ForceReplace)
					require.Nil(t, outcome.TerminationStatus)
					assert.Empty(t, outcome.Messages)
					ordinal = len(results[0].Messages)
					lastEntryUUID = results[0].Messages[ordinal-1].SourceUUID
					status = results[0].Session.TerminationStatus
					if status == TerminationAwaitingUser {
						waiting++
					}
					continue
				}
				require.Equal(t, IncrementalApplied, applied)
				if tc.tails[i] == duration || tc.tails[i] == pending {
					require.NotNil(t, outcome.TerminationStatus)
					assert.Empty(t, outcome.Messages)
				}
				assert.Equal(t, int64(len(tail)), outcome.ConsumedBytes)
				ordinal += len(outcome.Messages)
				lastEntryUUID = results[0].Messages[ordinal-1].SourceUUID
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
	t.Run("fork durations stay in their branch", func(t *testing.T) {
		const transcript = `{"type":"user","uuid":"u0","message":{"content":"hello"}}
{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}
{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"first"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"first answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"u2","parentUuid":"a1","message":{"content":"second"}}
{"type":"assistant","uuid":"a2","parentUuid":"u2","message":{"content":"second answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"u3","parentUuid":"a2","message":{"content":"third"}}
{"type":"assistant","uuid":"a3","parentUuid":"u3","message":{"content":"third answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"u4","parentUuid":"a3","message":{"content":"fourth"}}
{"type":"assistant","uuid":"a4","parentUuid":"u4","message":{"content":"main answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"uf","parentUuid":"a0","message":{"content":"fork"}}
{"type":"assistant","uuid":"af","parentUuid":"uf","timestamp":"2024-01-01T10:00:01Z","message":{"content":"fork answer","stop_reason":"end_turn"}}
`
		for _, tc := range []struct {
			name, tail  string
			main, fork  TerminationStatus
			forkEndedAt string
		}{
			{
				name: "delayed fork duration",
				tail: `{"type":"system","subtype":"stop_hook_summary","uuid":"sf","parentUuid":"af"}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"df","parentUuid":"sf","timestamp":"2024-01-01T11:00:00Z"}` + "\n",
				main: TerminationClean, fork: TerminationAwaitingUser,
				forkEndedAt: "2024-01-01T11:00:00Z",
			},
			{
				name: "fork has no duration",
				tail: `{"type":"system","subtype":"turn_duration","uuid":"dm","parentUuid":"a4"}` + "\n",
				main: TerminationAwaitingUser, fork: TerminationClean,
			},
			{
				name: "fork has pending agents",
				tail: `{"type":"system","subtype":"turn_duration","uuid":"df","parentUuid":"af","pendingBackgroundAgentCount":2}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"dm","parentUuid":"a4"}` + "\n",
				main: TerminationAwaitingUser, fork: TerminationClean,
			},
			{
				name: "fork finished before main duration",
				tail: `{"type":"system","subtype":"turn_duration","uuid":"df","parentUuid":"af"}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"dm","parentUuid":"a4","pendingBackgroundAgentCount":2}` + "\n",
				main: TerminationClean, fork: TerminationAwaitingUser,
			},
			{
				name: "hook summaries keep branch ownership",
				tail: `{"type":"system","subtype":"stop_hook_summary","uuid":"sf","parentUuid":"af"}` + "\n" + `{"type":"system","subtype":"stop_hook_summary","uuid":"sm","parentUuid":"a4"}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"df","parentUuid":"sf"}` + "\n" + `{"type":"system","subtype":"turn_duration","uuid":"dm","parentUuid":"sm","pendingWorkflowCount":1}` + "\n",
				main: TerminationClean, fork: TerminationAwaitingUser,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				content := testjsonl.ClaudeProducerJSONL(transcript+tc.tail, `"entrypoint":"cli","version":"2.1.266",`)
				path := filepath.Join(t.TempDir(), "session.jsonl")
				writeSourceFile(t, path, content)
				results, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, results, 2)
				assert.Equal(t, "session", results[0].Session.ID)
				assert.Equal(t, tc.main, results[0].Session.TerminationStatus)
				assert.Equal(t, "session-uf", results[1].Session.ID)
				assert.Equal(t, tc.fork, results[1].Session.TerminationStatus)
				if tc.forkEndedAt != "" {
					assert.Equal(t, tc.forkEndedAt, results[1].Session.EndedAt.Format(time.RFC3339))
				}
			})
		}
	})
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
			content = testjsonl.ClaudeProducerJSONL(content, `"entrypoint":"cli","version":"2.1.266",`)
			session, _ := runClaudeParserTest(t, "session.jsonl", content)
			assert.Equal(t, tc.want, session.TerminationStatus)
		})
	}
}
