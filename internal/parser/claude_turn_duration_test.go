package parser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestClaudeIncrementalWorkIndependentOfPrefix(t *testing.T) {
	const producer = `"entrypoint":"cli","version":"2.1.296",`
	const user = `{"type":"user","message":{"content":"hello"}}`
	const answer = `{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`
	const duration = `{"type":"system","subtype":"turn_duration"}`
	for _, after := range []bool{false, true} {
		var allocations []float64
		for _, size := range []int{10, 10000} {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			progress := strings.Repeat("{\"type\":\"progress\"}\n", size)
			prefix := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(user, answer), producer, 0)
			tail := duration + "\n"
			if after {
				prefix += duration + "\n" + progress
				tail = "{\"type\":\"progress\"}\n"
			} else {
				prefix = progress + prefix
			}
			writeSourceFile(t, path, prefix+tail)
			allocations = append(allocations, testing.AllocsPerRun(3, func() {
				var status *TerminationStatus
				var open *bool
				_, _, _, _, err := claudeParseSessionFrom(path, int64(len(prefix)), claudeIncrementalScan{
					termination: &status, storedTermination: new(TerminationAwaitingUser), turnOpen: &open, storedLinearParse: new(true), stored: claudeStoredIdentity{entrypoint: "cli"},
				})
				require.NoError(t, err)
				assert.Equal(t, new(TerminationAwaitingUser), status)
				if after {
					assert.Nil(t, open)
				} else {
					assert.Equal(t, new(false), open)
				}
			}))
		}
		assert.Less(t, allocations[1], allocations[0]+100, "append work must stay bounded as the prefix grows")
	}
}

func TestClaudeTurnDuration(t *testing.T) {
	const user = `{"type":"user","message":{"content":"hello"}}`
	const answer = `{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`
	const duration = `{"type":"system","subtype":"turn_duration"}`
	const pending = `{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":2}`
	const summary = `{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}`
	const streaming = `{"type":"assistant","message":{"content":"working","stop_reason":null}}`
	for _, tc := range []struct {
		name, producer string
		lines          []string
		status         TerminationStatus
		open           bool
	}{
		{name: "pending duration then deferred flush", lines: []string{user, answer, pending, user, answer, duration}, status: TerminationAwaitingUser},
		{name: "summary after deferred flush", lines: []string{user, answer, pending, user, answer, duration, summary}, status: TerminationAwaitingUser},
		{name: "own duration after deferred flush", lines: []string{user, answer, pending, user, answer, duration, summary, duration}, status: TerminationAwaitingUser},
		{name: "two hookless deferrals", lines: []string{user, answer, user, answer, duration}, status: TerminationAwaitingUser},
		{name: "pending agents", lines: []string{user, answer, pending}, status: TerminationAwaitingUser, open: true},
		{name: "pending workflow", lines: []string{user, answer, `{"type":"system","subtype":"turn_duration","pendingWorkflowCount":1}`}, status: TerminationAwaitingUser, open: true},
		{name: "zero counts", lines: []string{user, answer, `{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":0,"pendingWorkflowCount":0}`}, status: TerminationAwaitingUser},
		{name: "reply after duration", lines: []string{user, answer, duration, user, answer}, status: TerminationAwaitingUser, open: true},
		{name: "stream before duration", lines: []string{user, streaming}, status: TerminationClean, open: true},
		{name: "stream after duration", lines: []string{user, streaming, duration}, status: TerminationClean},
		{name: "duration after prompt", lines: []string{user, answer, user, duration}, status: TerminationClean},
		{name: "metadata after duration", lines: []string{user, answer, duration, `{"type":"user","isMeta":true,"message":{"content":"metadata"}}`}, status: TerminationAwaitingUser},
		{name: "oldest verified cli", producer: `"entrypoint":"cli","version":"2.1.259",`, lines: []string{user, answer}, status: TerminationAwaitingUser, open: true},
		{name: "older cli", producer: `"entrypoint":"cli","version":"2.1.200",`, lines: []string{user, answer}, status: TerminationAwaitingUser},
		{name: "headless", producer: `"entrypoint":"sdk-cli","version":"2.1.296",`, lines: []string{user, answer}, status: TerminationAwaitingUser},
		{name: "unversioned", producer: `"entrypoint":"cli",`, lines: []string{user, answer}, status: TerminationAwaitingUser},
		{name: "sidechain", producer: `"entrypoint":"cli","version":"2.1.296","isSidechain":true,`, lines: []string{user, answer}, status: TerminationAwaitingUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := tc.producer
			if producer == "" {
				producer = `"entrypoint":"cli","version":"2.1.296",`
			}
			session, _ := runClaudeParserTest(t, "session.jsonl", testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(tc.lines...), producer, 0))
			assert.Equal(t, tc.status, session.TerminationStatus)
			assert.Equal(t, new(tc.open), session.TurnOpen)
		})
	}
	for _, count := range []string{`"unknown"`, "0.5", "-1", "null", "false"} {
		t.Run("invalid pending count "+count, func(t *testing.T) {
			line := `{"type":"system","subtype":"turn_duration","pendingWorkflowCount":` + count + `}`
			session, _ := runClaudeParserTest(t, "session.jsonl", testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(user, answer, line), `"entrypoint":"cli","version":"2.1.296",`, 0))
			assert.Equal(t, TerminationAwaitingUser, session.TerminationStatus)
			assert.Equal(t, new(true), session.TurnOpen)
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
			assert.Equal(t, new(false), session.TurnOpen)
		})
	}
}

func TestClaudeIncrementalVerdictParity(t *testing.T) {
	const user = `{"type":"user","message":{"content":"hello"}}`
	const answer = `{"type":"assistant","message":{"content":"done","stop_reason":"end_turn"}}`
	const duration = `{"type":"system","subtype":"turn_duration"}`
	const summary = `{"type":"system","subtype":"stop_hook_summary","hookErrors":[]}`
	for _, tc := range []struct {
		name     string
		lines    []string
		partial  bool
		producer string
	}{
		{name: "parallel tools", lines: []string{
			user,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}],"stop_reason":"tool_use"}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"two"}]}}`, answer, duration,
		}},
		{name: "queued prompt during tools", lines: []string{user,
			`{"type":"assistant","timestamp":"2024-01-01T10:00:01Z","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":{}}],"stop_reason":"tool_use"}}`,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2024-01-01T10:00:02Z","content":"also inspect tests"}`,
			`{"type":"attachment","timestamp":"2024-01-01T10:00:02Z","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"also inspect tests"}}`,
			`{"type":"user","timestamp":"2024-01-01T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]}}`,
			`{"type":"assistant","timestamp":"2024-01-01T10:00:04Z","message":{"content":"done","stop_reason":"end_turn"}}`, duration,
		}},
		{name: "two hookless deferred turns", lines: []string{user, answer, user, answer, duration}},
		{name: "deferred swarm", lines: []string{user, answer, `{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":1}`, user, answer, duration, summary, duration}},
		{name: "streaming run", lines: []string{user,
			`{"type":"assistant","message":{"id":"reply","content":"working","stop_reason":null}}`,
			`{"type":"assistant","message":{"id":"reply","content":"done","stop_reason":"end_turn"}}`, summary, duration,
		}},
		{name: "discarded metadata between chunks", lines: []string{user,
			`{"type":"assistant","message":{"id":"reply","content":"working","stop_reason":null}}`,
			`{"type":"progress","data":{"type":"bash_progress","output":"still working"}}`,
			summary,
			`{"type":"attachment","attachment":{"type":"task_reminder","content":"pending"}}`,
			`{"type":"assistant","message":{"id":"reply","content":"done","stop_reason":"end_turn"}}`, duration,
		}},
		{name: "pending agents", lines: []string{user, answer, summary, `{"type":"system","subtype":"turn_duration","pendingBackgroundAgentCount":1}`, user, answer, duration}},
		{name: "older cli", producer: `"entrypoint":"cli","version":"2.1.200",`, lines: []string{user, answer}},
		{name: "assistant compact summary", lines: []string{user, answer, duration, `{"type":"assistant","isCompactSummary":true,"message":{"content":"summary"}}`, `{"type":"progress"}`}},
		{name: "tail without event", lines: []string{user, answer, duration, `{"type":"progress"}`}},
		{name: "partial final line", lines: []string{user, answer, summary, duration}, partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := tc.producer
			if producer == "" {
				producer = `"entrypoint":"cli","version":"2.1.296",`
			}
			content := testjsonl.ClaudeChainJSONL(t, testjsonl.JoinJSONL(tc.lines...), producer, 0)
			lines := strings.SplitAfter(content, "\n")
			path := filepath.Join(t.TempDir(), "session.jsonl")
			for end := 1; end < len(lines); end++ {
				complete := strings.Join(lines[:end], "")
				writeSourceFile(t, path, complete)
				full, err := parseClaudeSession(path, "project", "local")
				require.NoError(t, err)
				require.Len(t, full, 1)
				for split := 1; split <= end; split++ {
					writeSourceFile(t, path, strings.Join(lines[:split], ""))
					prefix, err := parseClaudeSession(path, "project", "local")
					require.NoError(t, err)
					require.Len(t, prefix, 1)
					writeSourceFile(t, path, complete)
					if tc.partial {
						writeSourceFile(t, path, complete+`{"type":"user"`)
					}
					var status *TerminationStatus
					var open *bool
					offset := int64(len(strings.Join(lines[:split], "")))
					_, _, _, consumed, err := claudeParseSessionFrom(path, offset, claudeIncrementalScan{
						termination: &status, storedTermination: new(prefix[0].Session.TerminationStatus), turnOpen: &open, stored: claudeStoredIdentity{entrypoint: "cli"}, storedLinearParse: new(true),
					})
					require.NoError(t, err, "split %d end %d", split, end)
					assert.Equal(t, new(full[0].Session.TerminationStatus), status, "split %d end %d", split, end)
					if split == end || (tc.name == "tail without event" && split == 3 && end == 4) {
						assert.Nil(t, open, "a tail without a turn event preserves storage")
					}
					if open == nil {
						open = prefix[0].Session.TurnOpen
					}
					assert.Equal(t, full[0].Session.TurnOpen, open, "split %d end %d", split, end)
					assert.Equal(t, int64(len(complete))-offset, consumed)
				}
			}
		})
	}
}
