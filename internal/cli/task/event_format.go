package task

import (
	"fmt"
	"strings"

	rt "github.com/docup/agentctl/internal/core/runtime"
)

func formatRuntimeEvent(ev rt.Event, raw bool, showThinking bool) (string, bool) {
	if raw {
		return formatRawRuntimeEvent(ev, showThinking)
	}
	return formatStructuredRuntimeEvent(ev, showThinking)
}

func formatRawRuntimeEvent(ev rt.Event, showThinking bool) (string, bool) {
	switch ev.EventType {
	case "stdout_line", "protocol_line":
		if !showThinking && looksLikeThinkingLine(ev.Details) {
			return "", false
		}
		return fmt.Sprintf("[%s] %s", ev.Timestamp.Format("15:04:05"), ev.Details), true
	case "stderr_line":
		return fmt.Sprintf("[%s] [stderr] %s", ev.Timestamp.Format("15:04:05"), ev.Details), true
	default:
		return "", false
	}
}

func formatStructuredRuntimeEvent(ev rt.Event, showThinking bool) (string, bool) {
	ts := ev.Timestamp.Format("15:04:05")
	stage := ""
	if ev.StageID != "" {
		stage = fmt.Sprintf(" [%s]", ev.StageID)
	}
	agent := ""
	if ev.AgentID != "" {
		agent = fmt.Sprintf(" (%s)", ev.AgentID)
	}

	switch ev.EventType {
	case "stdout_line", "stderr_line", "protocol_line":
		return "", false
	case "thinking":
		if !showThinking {
			return "", false
		}
		return fmt.Sprintf("[%s] thinking%s%s: %s", ts, stage, agent, ev.Details), true
	case "agent_message":
		return fmt.Sprintf("[%s] agent%s%s: %s", ts, stage, agent, ev.Details), true
	case "tool_call":
		return fmt.Sprintf("[%s] tool call%s%s: %s", ts, stage, agent, ev.Details), true
	case "tool_result":
		return fmt.Sprintf("[%s] tool result%s%s: %s", ts, stage, agent, ev.Details), true
	case "artifact_reported":
		return fmt.Sprintf("[%s] artifact%s%s: %s", ts, stage, agent, ev.Details), true
	case "progress":
		return fmt.Sprintf("[%s] progress%s%s: %s", ts, stage, agent, ev.Details), true
	case "runtime_violation":
		return fmt.Sprintf("[%s] runtime violation%s%s: %s", ts, stage, agent, ev.Details), true
	case "stage_started":
		return fmt.Sprintf("[%s] stage started%s%s: %s", ts, stage, agent, ev.Details), true
	case "agent_started":
		return fmt.Sprintf("[%s] agent started%s%s: %s", ts, stage, agent, ev.Details), true
	case "stage_completed":
		return fmt.Sprintf("[%s] stage completed%s%s: %s", ts, stage, agent, ev.Details), true
	case "stage_failed":
		return fmt.Sprintf("[%s] stage failed%s%s: %s", ts, stage, agent, ev.Details), true
	default:
		line := fmt.Sprintf("[%s] %s", ts, ev.EventType)
		if stage != "" {
			line += stage
		}
		if agent != "" {
			line += agent
		}
		if ev.Details != "" {
			line += ": " + ev.Details
		}
		return line, true
	}
}

func looksLikeThinkingLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return strings.Contains(line, `"type":"thinking"`) || strings.Contains(line, `"type": "thinking"`)
}
