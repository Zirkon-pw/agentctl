package taskrunner

import (
	"strings"
	"testing"

	. "github.com/docup/agentctl/internal/service/taskrunner"

	"github.com/docup/agentctl/internal/config/loader"
	rt "github.com/docup/agentctl/internal/core/runtime"
)

func resolveQwenDriver(t *testing.T, args []string) (loader.AgentDef, AgentCLIDriver) {
	t.Helper()

	registry := NewAgentDriverRegistry(&loader.AgentsConfig{
		Agents: []loader.AgentDef{{
			ID:      "qwen",
			Driver:  loader.AgentDriverQwen,
			Command: "qwen",
			Args:    args,
		}},
	})

	profile, driver, err := registry.Resolve("qwen")
	if err != nil {
		t.Fatalf("resolve qwen driver: %v", err)
	}
	return profile, driver
}

func countArg(args []string, target string) int {
	count := 0
	for _, arg := range args {
		if arg == target {
			count++
		}
	}
	return count
}

func TestQwenBuildInvocation_NormalizesStructuredFlags(t *testing.T) {
	profile, driver := resolveQwenDriver(t, []string{"--output-format=stream-json", "--yolo", "--model", "qwen3-coder-plus"})

	invocation, err := driver.BuildInvocation(profile, &rt.StageSpec{}, &rt.RunSession{}, "hello")
	if err != nil {
		t.Fatalf("build invocation: %v", err)
	}

	if countArg(invocation.Args, "--yolo") != 1 {
		t.Fatalf("expected exactly one --yolo flag, got %v", invocation.Args)
	}
	if countArg(invocation.Args, "--output-format") != 1 {
		t.Fatalf("expected exactly one --output-format flag, got %v", invocation.Args)
	}
	if !strings.Contains(strings.Join(invocation.Args, " "), "--output-format stream-json") {
		t.Fatalf("expected stream-json output format, got %v", invocation.Args)
	}
	if !strings.Contains(strings.Join(invocation.Args, " "), "--model qwen3-coder-plus") {
		t.Fatalf("expected custom args to be preserved, got %v", invocation.Args)
	}
}

func TestQwenBuildInvocation_PrefersResumeOverContinue(t *testing.T) {
	profile, driver := resolveQwenDriver(t, nil)

	invocation, err := driver.BuildInvocation(profile, &rt.StageSpec{}, &rt.RunSession{
		DriverState: rt.DriverState{ExternalSessionID: "session-123"},
		StageHistory: []rt.StageRun{
			{StageID: "STAGE-001"},
		},
	}, "hello")
	if err != nil {
		t.Fatalf("build invocation: %v", err)
	}

	argsJoined := strings.Join(invocation.Args, " ")
	if !strings.Contains(argsJoined, "--resume session-123") {
		t.Fatalf("expected resume flag, got %v", invocation.Args)
	}
	if strings.Contains(argsJoined, "--continue") {
		t.Fatalf("did not expect continue flag when resume is available, got %v", invocation.Args)
	}
}

func TestQwenParseStageOutput_ExplainsPlainTextFailure(t *testing.T) {
	_, driver := resolveQwenDriver(t, nil)

	_, err := driver.ParseStageOutput(&rt.StageSpec{Type: rt.StageTypeExecute}, &StageCapture{
		Stdout: "Done without structured output",
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "qwen output is not valid JSON") {
		t.Fatalf("expected JSON parse error, got %v", err)
	}
}

func TestQwenParseStageOutput_AcceptsJSONArray(t *testing.T) {
	_, driver := resolveQwenDriver(t, nil)

	out, err := driver.ParseStageOutput(&rt.StageSpec{Type: rt.StageTypeExecute}, &StageCapture{
		Stdout: `[
  {
    "type":"system",
    "session_id":"qwen-session-1"
  },
  {
    "type":"assistant",
    "session_id":"qwen-session-1",
    "message":{
      "content":[
        {
          "type":"text",
          "text":"AGENTCTL_RESULT_BEGIN\n{\"outcome\":\"completed\",\"summary\":\"# Summary\\nDone from array\"}\nAGENTCTL_RESULT_END"
        }
      ]
    }
  }
]`,
	})
	if err != nil {
		t.Fatalf("parse stage output: %v", err)
	}
	if out.Result.Outcome != "completed" {
		t.Fatalf("expected completed outcome, got %+v", out.Result)
	}
	if out.StructuredLogName != "qwen.response.json" {
		t.Fatalf("expected json structured log name, got %q", out.StructuredLogName)
	}
}

func TestParseQwenLiveEvents_ExtractsStructuredTimeline(t *testing.T) {
	spec := &rt.StageSpec{
		TaskID:    "TASK-001",
		RunID:     "RUN-001",
		SessionID: "RUN-001",
		StageID:   "STAGE-001",
		AgentID:   "qwen",
	}

	assistantLine := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"reasoning"},{"type":"text","text":"I'll create the file"},{"type":"tool_use","id":"call_1","name":"write_file","input":{"file_path":"/tmp/out.txt"}}]}}`
	userLine := `{"type":"user","content":[{"type":"tool_result","tool_use_id":"call_1","is_error":false,"content":"done"}]}`

	assistantEvents, err := ParseQwenLiveEvents(spec, assistantLine)
	if err != nil {
		t.Fatalf("assistant parse: %v", err)
	}
	userEvents, err := ParseQwenLiveEvents(spec, userLine)
	if err != nil {
		t.Fatalf("user parse: %v", err)
	}

	if len(assistantEvents) != 4 {
		t.Fatalf("expected 4 assistant events, got %d: %+v", len(assistantEvents), assistantEvents)
	}
	if assistantEvents[0].EventType != "thinking" {
		t.Fatalf("expected thinking event first, got %+v", assistantEvents[0])
	}
	if assistantEvents[1].EventType != "agent_message" {
		t.Fatalf("expected agent message event, got %+v", assistantEvents[1])
	}
	if assistantEvents[2].EventType != "tool_call" {
		t.Fatalf("expected tool call event, got %+v", assistantEvents[2])
	}
	if assistantEvents[3].EventType != "artifact_reported" {
		t.Fatalf("expected artifact event, got %+v", assistantEvents[3])
	}
	if len(userEvents) != 1 || userEvents[0].EventType != "tool_result" {
		t.Fatalf("expected tool result event, got %+v", userEvents)
	}
}
