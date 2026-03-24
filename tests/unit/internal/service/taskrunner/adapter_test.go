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
	if strings.Contains(strings.Join(invocation.Args, " "), "stream-json") {
		t.Fatalf("expected output format override to be removed, got %v", invocation.Args)
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
