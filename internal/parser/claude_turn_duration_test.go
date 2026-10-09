package parser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestClaudeDeferredSwarmDuration(t *testing.T) {
	const producer = `"entrypoint":"cli","version":"2.1.296",`
	lines := []string{
		`{"type":"user","message":{"content":"first"}}`,
		`{"type":"assistant","message":{"content":"first reply","stop_reason":"end_turn"}}`,
		`{"type":"user","message":{"content":"second"}}`,
		`{"type":"assistant","message":{"content":"second reply","stop_reason":"end_turn"}}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}`,
		`{"type":"system","subtype":"turn_duration"}`,
	}
	for _, end := range []int{5, 6, 7} {
		session, _ := runClaudeParserTest(t, "session.jsonl", testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(lines[:end]...), producer, 0))
		assert.Equal(t, TerminationAwaitingUser, session.TerminationStatus)
		assert.Equal(t, new(end < 7), session.TurnOpen)
	}
}

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
			content := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(user, answer, duration, user), producer, 0)
			for i, step := range tc.steps {
				content += testjsonl.ClaudeChainJSONL(t, step.line+"\n", producer, strings.Count(content, "\n"))
				session, _ := runClaudeParserTest(t, "session.jsonl", content)
				assert.Equal(t, step.status, session.TerminationStatus, "record %d", i)
				assert.Equal(t, new(step.open), session.TurnOpen, "record %d", i)
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

func TestClaudeIncrementalVerdictParity(t *testing.T) {
	const user = `{"type":"user","message":{"content":"hello"}}`
	const answer = `{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`
	const duration = `{"type":"system","subtype":"turn_duration"}`
	const summary = `{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}`
	for _, tc := range []struct {
		name    string
		lines   []string
		partial bool
	}{
		{name: "parallel tools", lines: []string{
			user,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}],"stop_reason":"tool_use"}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"two"}]}}`, answer, duration,
		}},
		{name: "deferred swarm", lines: []string{user, answer, user, answer, duration, summary, duration}},
		{name: "streaming run", lines: []string{user,
			`{"type":"assistant","message":{"id":"reply","content":"working","stop_reason":null}}`,
			`{"type":"assistant","message":{"id":"reply","content":"done","stop_reason":"end_turn"}}`, summary, duration,
		}},
		{name: "pending agents", lines: []string{user, answer, summary, `{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":1}`, user, answer, duration}},
		{name: "blocked hook", lines: []string{user, answer, user, answer, `{"type":"system","subtype":"stop_hook_summary","hookErrors":["blocked"]}`, duration, answer, summary, duration}},
		{name: "hooked double deferral", lines: []string{user, answer, user, answer, summary, user, answer, duration, summary, duration}},
		{name: "partial final line", lines: []string{user, answer, summary, duration}, partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(tc.lines...), `"entrypoint":"cli","version":"2.1.296",`, 0)
			lines := strings.SplitAfter(content, "\n")
			path := filepath.Join(t.TempDir(), "session.jsonl")
			for end := 1; end < len(lines); end++ {
				complete := strings.Join(lines[:end], "")
				writeSourceFile(t, path, complete)
				full, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, full, 1)
				if tc.partial {
					writeSourceFile(t, path, complete+`{"type":"user"`)
				}
				for split := 1; split <= end; split++ {
					var status *TerminationStatus
					var open *bool
					offset := int64(len(strings.Join(lines[:split], "")))
					_, _, _, consumed, err := claudeParseSessionFrom(path, offset, claudeIncrementalScan{
						termination: &status, turnOpen: &open, stored: claudeStoredIdentity{entrypoint: "cli"}, storedLinearParse: new(true),
					})
					require.NoError(t, err, "split %d end %d", split, end)
					assert.Equal(t, new(full[0].Session.TerminationStatus), status, "split %d end %d", split, end)
					assert.Equal(t, full[0].Session.TurnOpen, open, "split %d end %d", split, end)
					assert.Equal(t, int64(len(complete))-offset, consumed)
				}
			}
		})
	}
}
