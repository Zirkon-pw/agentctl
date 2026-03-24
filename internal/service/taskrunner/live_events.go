package taskrunner

import (
	"encoding/json"
	"fmt"
	"strings"

	rt "github.com/docup/agentctl/internal/core/runtime"
)

func buildStageEvent(spec *rt.StageSpec, eventType, details string) rt.Event {
	return rt.Event{
		TaskID:    spec.TaskID,
		RunID:     spec.RunID,
		SessionID: spec.SessionID,
		StageID:   spec.StageID,
		AgentID:   spec.AgentID,
		EventType: eventType,
		Details:   details,
	}
}

func ParseQwenLiveEvents(spec *rt.StageSpec, line string) ([]rt.Event, error) {
	var item map[string]any
	if err := json.Unmarshal([]byte(line), &item); err != nil {
		return nil, err
	}

	switch item["type"] {
	case "system":
		if subtype, _ := item["subtype"].(string); subtype != "" {
			return []rt.Event{buildStageEvent(spec, "progress", fmt.Sprintf("system:%s", subtype))}, nil
		}
		return nil, nil
	case "assistant":
		return parseQwenMessageBlocks(spec, item["message"])
	case "user":
		return parseQwenUserBlocks(spec, item["content"])
	case "result":
		if subtype, _ := item["subtype"].(string); subtype != "" {
			return []rt.Event{buildStageEvent(spec, "progress", fmt.Sprintf("result:%s", subtype))}, nil
		}
	}
	return nil, nil
}

func parseQwenMessageBlocks(spec *rt.StageSpec, message any) ([]rt.Event, error) {
	msg, ok := message.(map[string]any)
	if !ok {
		return nil, nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil, nil
	}
	return parseQwenContentBlocks(spec, content)
}

func parseQwenUserBlocks(spec *rt.StageSpec, content any) ([]rt.Event, error) {
	blocks, ok := content.([]any)
	if !ok {
		return nil, nil
	}

	events := make([]rt.Event, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] != "tool_result" {
			continue
		}
		details := describeQwenToolResult(block)
		if details == "" {
			continue
		}
		events = append(events, buildStageEvent(spec, "tool_result", details))
	}
	return events, nil
}

func parseQwenContentBlocks(spec *rt.StageSpec, content []any) ([]rt.Event, error) {
	events := make([]rt.Event, 0, len(content))
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			text = strings.TrimSpace(text)
			if text == "" || strings.Contains(text, resultBeginMarker) {
				continue
			}
			events = append(events, buildStageEvent(spec, "agent_message", text))
		case "thinking":
			thinking, _ := block["thinking"].(string)
			thinking = strings.TrimSpace(thinking)
			if thinking == "" {
				continue
			}
			events = append(events, buildStageEvent(spec, "thinking", thinking))
		case "tool_use":
			details := describeQwenToolUse(block)
			if details != "" {
				events = append(events, buildStageEvent(spec, "tool_call", details))
			}
			if artifact := describeQwenArtifact(block); artifact != "" {
				events = append(events, buildStageEvent(spec, "artifact_reported", artifact))
			}
		}
	}
	return events, nil
}

func describeQwenToolUse(block map[string]any) string {
	name, _ := block["name"].(string)
	input, _ := block["input"].(map[string]any)
	if name == "" {
		return ""
	}

	switch name {
	case "run_shell_command":
		if cmd, _ := input["command"].(string); cmd != "" {
			return fmt.Sprintf("%s %s", name, truncateForEvent(cmd, 180))
		}
	case "write_file":
		if path, _ := input["file_path"].(string); path != "" {
			return fmt.Sprintf("%s %s", name, path)
		}
	case "edit":
		if path, _ := input["file_path"].(string); path != "" {
			return fmt.Sprintf("%s %s", name, path)
		}
	}

	payload, err := json.Marshal(input)
	if err != nil || len(payload) == 0 {
		return name
	}
	return fmt.Sprintf("%s %s", name, truncateForEvent(string(payload), 180))
}

func describeQwenArtifact(block map[string]any) string {
	name, _ := block["name"].(string)
	input, _ := block["input"].(map[string]any)
	switch name {
	case "write_file", "edit":
		if path, _ := input["file_path"].(string); path != "" {
			return path
		}
	}
	return ""
}

func describeQwenToolResult(block map[string]any) string {
	status := "ok"
	if isError, _ := block["is_error"].(bool); isError {
		status = "error"
	}

	content := formatQwenToolResultContent(block["content"])
	if content == "" {
		if toolID, _ := block["tool_use_id"].(string); toolID != "" {
			return fmt.Sprintf("%s %s", status, toolID)
		}
		return status
	}
	return fmt.Sprintf("%s %s", status, truncateForEvent(content, 180))
}

func formatQwenToolResultContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			switch block := item.(type) {
			case string:
				parts = append(parts, strings.TrimSpace(block))
			case map[string]any:
				if text, _ := block["text"].(string); text != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
}

func truncateForEvent(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
