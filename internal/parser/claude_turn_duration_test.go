package parser

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestClaudeTurnDuration(t *testing.T) {
	const user = `{"type":"user","message":{"content":"hello"}}`
	const answer = `{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`
	const duration = `{"type":"system","subtype":"turn_duration"}`
	type step struct {
		line   string
		status TerminationStatus
		open   bool
	}
	for _, tc := range []struct {
		name, producer string
		steps          []step
	}{
		{name: "pending agents and stop hooks", steps: []step{
			{answer, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"stop_hook_summary","hookErrors":["blocked"]}`, TerminationAwaitingUser, true},
			{answer, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":2}`, TerminationAwaitingUser, true},
			{`{"type":"user","message":{"content":"<task-notification>agent finished</task-notification>"}}`, TerminationAwaitingUser, true},
			{answer, TerminationAwaitingUser, true},
			{duration, TerminationAwaitingUser, false},
		}},
		{name: "workflow and invalid counts", steps: []step{
			{answer, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":1}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":"unknown"}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":0.5}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":-1}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":null}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":false}`, TerminationAwaitingUser, true},
			{`{"type":"system","subtype":"turn_duration","pendingWorkflowCount":0,"pendingBackgroundAgentCount":0}`, TerminationAwaitingUser, false},
		}},
		{name: "deferred duration during stream", steps: []step{
			{answer, TerminationAwaitingUser, true},
			{duration, TerminationAwaitingUser, false},
			{user, TerminationClean, false},
			{`{"type":"assistant","message":{"content":"working","stop_reason":null}}`, TerminationClean, true},
			{duration, TerminationClean, true},
			{answer, TerminationAwaitingUser, true},
			{duration, TerminationAwaitingUser, false},
		}},
		{name: "duration after next prompt", steps: []step{
			{answer, TerminationAwaitingUser, true},
			{user + "\n" + duration, TerminationClean, true},
			{answer, TerminationAwaitingUser, true},
			{duration, TerminationAwaitingUser, false},
		}},
		{name: "filtered metadata", steps: []step{
			{answer, TerminationAwaitingUser, true},
			{`{"type":"user","isMeta":true,"message":{"content":"metadata"}}`, TerminationAwaitingUser, true},
			{`{"type":"attachment","attachment":{"type":"file","filename":"example.txt"}}`, TerminationAwaitingUser, true},
			{duration, TerminationAwaitingUser, false},
		}},
		{name: "duration before reply", steps: []step{{duration, TerminationClean, false}, {answer, TerminationAwaitingUser, true}, {duration, TerminationAwaitingUser, false}}},
		{name: "oldest verified cli", producer: `"entrypoint":"cli","version":"2.1.259",`, steps: []step{{answer, TerminationAwaitingUser, true}, {duration, TerminationAwaitingUser, false}}},
		{name: "older cli", producer: `"entrypoint":"cli","version":"2.1.200",`, steps: []step{{answer, TerminationAwaitingUser, false}}},
		{name: "headless", producer: `"entrypoint":"sdk-cli","version":"2.1.266",`, steps: []step{{answer, TerminationAwaitingUser, false}, {duration, TerminationAwaitingUser, false}}},
		{name: "unversioned", producer: `"entrypoint":"cli",`, steps: []step{{answer, TerminationAwaitingUser, false}}},
		{name: "sidechain", producer: `"entrypoint":"cli","version":"2.1.266","isSidechain":true,`, steps: []step{{answer, TerminationAwaitingUser, false}, {duration, TerminationAwaitingUser, false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := tc.producer
			if producer == "" {
				producer = `"entrypoint":"cli","version":"2.1.266",`
			}
			root := t.TempDir()
			path := filepath.Join(root, "project", "session.jsonl")
			content := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(user, answer, duration, user), producer, 0)
			writeSourceFile(t, path, content)
			provider, ok := NewProvider(AgentClaude, ProviderConfig{Roots: []string{root}})
			require.True(t, ok)
			source, ok, err := provider.FindSource(t.Context(), FindSourceRequest{RawSessionID: "session"})
			require.NoError(t, err)
			require.True(t, ok)
			for i, step := range tc.steps {
				// Build the request before appending so full-parse state cannot hide a bad carry.
				before, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, before, 1)
				stored := before[0].Session
				lastUUID := ""
				for _, msg := range slices.Backward(before[0].Messages) {
					if msg.SourceUUID != "" {
						lastUUID = msg.SourceUUID
						break
					}
				}
				req := IncrementalRequest{
					Source: source, SessionID: "session", Offset: int64(len(content)),
					StartOrdinal: len(before[0].Messages), LastEntryUUID: lastUUID,
					StoredEntrypoint: stored.Entrypoint, StoredSessionKind: stored.SessionKind,
					StoredClaudeLinearParse: stored.ClaudeLinearParse,
					StoredTerminationStatus: string(stored.TerminationStatus), StoredUserMessageCount: stored.UserMessageCount,
				}
				tail := testjsonl.ClaudeChainJSONL(t, step.line+"\n", producer, strings.Count(content, "\n"))
				content += tail
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				req.Fingerprint = SourceFingerprint{Key: path, Size: int64(len(content))}
				full, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, full, 1)
				assert.Equal(t, step.status, full[0].Session.TerminationStatus, "full record %d", i)
				assert.Equal(t, new(step.open), full[0].Session.TurnOpen, "full record %d", i)
				outcome, applied, err := provider.ParseIncremental(t.Context(), req)
				require.NoError(t, err)
				require.Equal(t, IncrementalApplied, applied, "record %d", i)
				assert.Equal(t, int64(len(tail)), outcome.ConsumedBytes)
				require.NotNil(t, outcome.TerminationStatus)
				assert.Equal(t, step.status, *outcome.TerminationStatus, "append record %d", i)
				open := stored.TurnOpen
				if outcome.TurnOpen != nil {
					open = outcome.TurnOpen
				}
				assert.Equal(t, new(step.open), open, "append record %d", i)
				assert.Len(t, full[0].Messages, len(before[0].Messages)+len(outcome.Messages))
			}
		})
	}
	t.Run("fork duration precedes main reply", func(t *testing.T) {
		const transcript = `{"type":"user","uuid":"u0","message":{"content":"hello"}}
{"type":"assistant","uuid":"a0","parentUuid":"u0","message":{"content":"ready","stop_reason":"end_turn"}}
{"type":"user","uuid":"u1","parentUuid":"a0","message":{"content":"first"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":"first answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"u2","parentUuid":"a1","message":{"content":"second"}}
{"type":"assistant","uuid":"a2","parentUuid":"u2","message":{"content":"second answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"u3","parentUuid":"a2","message":{"content":"third"}}
{"type":"assistant","uuid":"a3","parentUuid":"u3","message":{"content":"third answer","stop_reason":"end_turn"}}
{"type":"user","uuid":"uf","parentUuid":"a0","message":{"content":"fork"}}
{"type":"assistant","uuid":"af","parentUuid":"uf","message":{"content":"fork answer","stop_reason":"end_turn"}}
{"type":"system","subtype":"turn_duration"}
{"type":"user","uuid":"u4","parentUuid":"a3","message":{"content":"fourth"}}
{"type":"assistant","uuid":"a4","parentUuid":"u4","message":{"content":"main answer","stop_reason":"end_turn"}}
`
		path := filepath.Join(t.TempDir(), "session.jsonl")
		writeSourceFile(t, path, testjsonl.ClaudeProducerJSONL(transcript, `"entrypoint":"cli","version":"2.1.266",`))
		results, err := parseClaudeSession(path, "project", "local")
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, "session", results[0].Session.ID)
		assert.Equal(t, TerminationAwaitingUser, results[0].Session.TerminationStatus)
		assert.Equal(t, new(true), results[0].Session.TurnOpen)
		assert.Equal(t, "session-uf", results[1].Session.ID)
		assert.Equal(t, TerminationAwaitingUser, results[1].Session.TerminationStatus)
		assert.Equal(t, new(false), results[1].Session.TurnOpen)
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
			partial := ""
			if tc.name == "truncated" {
				partial = `{"type":"user"`
				content = strings.TrimSuffix(content, partial)
			}
			content = testjsonl.ClaudeChainJSONL(t, content, `"entrypoint":"cli","version":"2.1.266",`, 0) + partial
			session, _ := runClaudeParserTest(t, "session.jsonl", content)
			assert.Equal(t, tc.want, session.TerminationStatus)
		})
	}
}

func TestClaudeIncrementalParallelTools(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":"read both"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}],"stop_reason":"tool_use"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"two"}]}}`,
		`{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`,
	}
	fullStatuses := []TerminationStatus{TerminationClean, TerminationToolCallPending, TerminationToolCallPending, TerminationClean, TerminationAwaitingUser}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	for boundary := 1; boundary < len(lines); boundary++ {
		prefix := strings.Join(lines[:boundary], "\n") + "\n"
		writeSourceFile(t, path, prefix)
		before, err := parseClaudeSession(path, "project", "local")
		require.NoError(t, err)
		require.Len(t, before, 1)
		for end := boundary + 1; end <= len(lines); end++ {
			content := strings.Join(lines[:end], "\n") + "\n"
			writeSourceFile(t, path, content)
			full, err := parseClaudeSession(path, "project", "local")
			require.NoError(t, err)
			require.Len(t, full, 1)
			assert.Equal(t, fullStatuses[end-1], full[0].Session.TerminationStatus)
			var status *TerminationStatus
			_, _, _, _, err = claudeParseSessionFrom(path, int64(len(prefix)), claudeIncrementalScan{
				termination: &status, startOrdinal: len(before[0].Messages), storedStatus: before[0].Session.TerminationStatus,
			})
			require.NoError(t, err)
			if boundary >= 2 && end < 5 {
				assert.Nil(t, status, "partial tool context remains unknown")
			} else {
				assert.Equal(t, new(fullStatuses[end-1]), status, "boundary %d end %d", boundary, end)
			}
		}
	}
}

func TestClaudePendingAgentsPreserveStatus(t *testing.T) {
	content := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("hello", tsEarly),
		testjsonl.ClaudeAssistantJSON("done", tsEarlyS1, "end_turn"),
		`{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":1}`,
	), `"entrypoint":"cli","version":"2.1.266",`, 0)
	session, _ := runClaudeParserTest(t, "session.jsonl", content)
	assert.Equal(t, TerminationAwaitingUser, session.TerminationStatus)
	assert.Equal(t, new(true), session.TurnOpen)
}
