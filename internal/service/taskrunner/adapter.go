package taskrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/docup/agentctl/internal/config/loader"
	"github.com/docup/agentctl/internal/core/clarification"
	rt "github.com/docup/agentctl/internal/core/runtime"
)

const (
	resultBeginMarker = "AGENTCTL_RESULT_BEGIN"
	resultEndMarker   = "AGENTCTL_RESULT_END"
)

// AgentCLIDriver encapsulates CLI-specific invocation and output parsing logic.
type AgentCLIDriver interface {
	Name() loader.AgentDriver
	SupportsStage(rt.StageType) bool
	BuildStagePrompt(basePrompt string, spec *rt.StageSpec, session *rt.RunSession) (string, error)
	BuildInvocation(profile loader.AgentDef, spec *rt.StageSpec, session *rt.RunSession, prompt string) (*CLIInvocation, error)
	ParseStageOutput(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error)
}

// CLIInvocation is a concrete process invocation produced by a driver.
type CLIInvocation struct {
	Command           string
	Args              []string
	Env               []string
	StructuredLogName string
}

// ParsedStageOutput is the normalized outcome produced by a CLI driver.
type ParsedStageOutput struct {
	Result            rt.StageResult
	Summary           string
	ReviewReport      *rt.ReviewReport
	Clarification     *ParsedClarification
	StructuredLogName string
	StructuredLog     []byte
	ExternalSessionID string
}

// ParsedClarification is the normalized clarification request extracted from CLI output.
type ParsedClarification struct {
	RequestID   string
	Reason      string
	Questions   []clarification.Question
	ContextRefs []string
}

// StageCapture contains the raw captured process output.
type StageCapture struct {
	Stdout     string
	Stderr     string
	ProcessErr error
}

// DriverHandle represents a live CLI process.
type DriverHandle interface {
	PID() int
	ProcessGroupID() int
	Stdout() <-chan string
	Stderr() <-chan string
	Done() <-chan error
	Stop() error
	Kill() error
}

// AgentDriverRegistry resolves configured agent profiles to built-in CLI drivers.
type AgentDriverRegistry struct {
	profiles map[string]loader.AgentDef
	drivers  map[loader.AgentDriver]AgentCLIDriver
}

// NewAgentDriverRegistry creates a code-first driver registry from agents.yaml profiles.
func NewAgentDriverRegistry(cfg *loader.AgentsConfig) *AgentDriverRegistry {
	profiles := make(map[string]loader.AgentDef, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		profiles[agent.ID] = agent
	}
	return &AgentDriverRegistry{
		profiles: profiles,
		drivers: map[loader.AgentDriver]AgentCLIDriver{
			loader.AgentDriverClaude:  newClaudeDriver(),
			loader.AgentDriverCodex:   newCodexDriver(),
			loader.AgentDriverQwen:    newQwenDriver(),
			loader.AgentDriverGeneric: newGenericDriver(),
		},
	}
}

// AgentRuntimeRegistry is a compatibility alias for the old registry name.
type AgentRuntimeRegistry = AgentDriverRegistry

// NewAgentRuntimeRegistry preserves the old constructor name while returning the new code-first registry.
func NewAgentRuntimeRegistry(cfg *loader.AgentsConfig) *AgentRuntimeRegistry {
	return NewAgentDriverRegistry(cfg)
}

// Resolve returns the configured profile and its built-in driver.
func (r *AgentDriverRegistry) Resolve(agentID string) (loader.AgentDef, AgentCLIDriver, error) {
	profile, ok := r.profiles[agentID]
	if !ok {
		return loader.AgentDef{}, nil, fmt.Errorf("unknown agent: %s", agentID)
	}
	driver, ok := r.drivers[profile.Driver]
	if !ok {
		return loader.AgentDef{}, nil, fmt.Errorf("agent %s uses unsupported driver %q", agentID, profile.Driver)
	}
	return profile, driver, nil
}

// Start launches the CLI invocation as a managed process.
func StartCLIProcess(ctx context.Context, spec *rt.StageSpec, invocation *CLIInvocation, profile loader.AgentDef) (DriverHandle, error) {
	// Pre-validate that the command exists.
	if _, err := exec.LookPath(invocation.Command); err != nil {
		return nil, fmt.Errorf("agent command not found: %s (ensure it is installed and in PATH)", invocation.Command)
	}

	cmd := exec.CommandContext(ctx, invocation.Command, invocation.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(os.Environ(), buildStageEnv(spec, profile)...)
	cmd.Env = append(cmd.Env, invocation.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("opening stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s driver process: %w", profile.ID, err)
	}

	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	handle := &driverHandle{
		cmd:    cmd,
		pgid:   pgid,
		stdout: make(chan string, 64),
		stderr: make(chan string, 64),
		done:   make(chan error, 1),
	}
	go handle.readLines(stdout, handle.stdout)
	go handle.readLines(stderr, handle.stderr)
	go handle.wait()
	return handle, nil
}

type driverHandle struct {
	cmd    *exec.Cmd
	pgid   int
	stdout chan string
	stderr chan string
	done   chan error
	mu     sync.Mutex
}

func (h *driverHandle) PID() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

func (h *driverHandle) ProcessGroupID() int   { return h.pgid }
func (h *driverHandle) Stdout() <-chan string { return h.stdout }
func (h *driverHandle) Stderr() <-chan string { return h.stderr }
func (h *driverHandle) Done() <-chan error    { return h.done }

func (h *driverHandle) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pgid > 0 {
		return syscall.Kill(-h.pgid, syscall.SIGTERM)
	}
	if h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Signal(syscall.SIGTERM)
}

func (h *driverHandle) Kill() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pgid > 0 {
		return syscall.Kill(-h.pgid, syscall.SIGKILL)
	}
	if h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Kill()
}

func (h *driverHandle) readLines(r io.Reader, dst chan string) {
	defer close(dst)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		dst <- scanner.Text()
	}
}

func (h *driverHandle) wait() {
	err := h.cmd.Wait()
	h.done <- err
	close(h.done)
}

// ---------------------------------------------------------------------------
// Envelope and output parsing
// ---------------------------------------------------------------------------

type cliEnvelope struct {
	Outcome     string                   `json:"outcome"`
	Message     string                   `json:"message,omitempty"`
	Summary     string                   `json:"summary,omitempty"`
	NextAgentID string                   `json:"next_agent_id,omitempty"`
	Reason      string                   `json:"reason,omitempty"`
	RequestID   string                   `json:"request_id,omitempty"`
	Questions   []clarification.Question `json:"questions,omitempty"`
	ContextRefs []string                 `json:"context_refs,omitempty"`
	Findings    []rt.ReviewFinding       `json:"findings,omitempty"`
}

// ---------------------------------------------------------------------------
// baseDriver — shared logic for all drivers
// ---------------------------------------------------------------------------

type baseDriver struct {
	name loader.AgentDriver
}

func (d *baseDriver) Name() loader.AgentDriver              { return d.name }
func (d *baseDriver) SupportsStage(stage rt.StageType) bool { return d.supportsStage(stage) }

func (d *baseDriver) BuildStagePrompt(basePrompt string, spec *rt.StageSpec, session *rt.RunSession) (string, error) {
	return d.buildFallbackPrompt(basePrompt, spec.Type), nil
}

func (d *baseDriver) supportsStage(stage rt.StageType) bool {
	switch stage {
	case rt.StageTypeExecute, rt.StageTypeClarify, rt.StageTypeValidateFix, rt.StageTypeReview:
		return true
	default:
		return false
	}
}

func (d *baseDriver) buildFallbackPrompt(basePrompt string, stageType rt.StageType) string {
	return strings.TrimSpace(basePrompt) + "\n\n" + strings.TrimSpace(fmt.Sprintf(`
Return your final machine-readable stage result as a JSON object wrapped between these exact markers:
%s
{ ... }
%s

Rules:
- The JSON object must be the final thing you output inside the markers.
- Do not wrap the JSON in markdown fences.
- Keep all human explanation outside the markers.

For %s stage use this schema:
- Common fields: outcome, message.
- outcome for execution-like stages: completed, clarification_requested, handoff_requested, failed.
- outcome for review stage: completed or failed.
- summary: optional human summary text.
- next_agent_id: required only for handoff_requested.
- reason, request_id, questions, context_refs: only for clarification_requested.
- findings: only for review stage, as an array of review findings.
`, resultBeginMarker, resultEndMarker, stageType))
}

func (d *baseDriver) parseEnvelopeFromText(text string) (*cliEnvelope, error) {
	payload, err := extractResultEnvelope(text)
	if err != nil {
		return nil, err
	}
	var env cliEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("parsing structured CLI result: %w", err)
	}
	if env.Outcome == "" {
		return nil, fmt.Errorf("structured CLI result is missing outcome")
	}
	return &env, nil
}

func (d *baseDriver) parsedOutputFromEnvelope(stageType rt.StageType, env *cliEnvelope) *ParsedStageOutput {
	out := &ParsedStageOutput{
		Result: rt.StageResult{
			Outcome:     env.Outcome,
			Message:     env.Message,
			NextAgentID: env.NextAgentID,
		},
		Summary: env.Summary,
	}
	if env.Outcome == "clarification_requested" {
		out.Clarification = &ParsedClarification{
			RequestID:   env.RequestID,
			Reason:      env.Reason,
			Questions:   env.Questions,
			ContextRefs: env.ContextRefs,
		}
	}
	if stageType == rt.StageTypeReview {
		out.ReviewReport = &rt.ReviewReport{
			Summary:  env.Summary,
			Findings: env.Findings,
		}
	}
	return out
}

// parseStageOutputWithEnvelope is a shared implementation for drivers using standard envelope parsing.
func (d *baseDriver) parseStageOutputWithEnvelope(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error) {
	env, err := d.parseEnvelopeFromText(capture.Stdout)
	if err != nil {
		return nil, err
	}
	return d.parsedOutputFromEnvelope(spec.Type, env), nil
}

// ---------------------------------------------------------------------------
// Claude driver
// ---------------------------------------------------------------------------

type claudeDriver struct{ baseDriver }

func newClaudeDriver() AgentCLIDriver {
	return &claudeDriver{baseDriver{name: loader.AgentDriverClaude}}
}

func (d *claudeDriver) BuildInvocation(profile loader.AgentDef, spec *rt.StageSpec, session *rt.RunSession, prompt string) (*CLIInvocation, error) {
	args := append([]string{}, profile.Args...)
	args = append(args, "-p", prompt)
	return &CLIInvocation{Command: profile.Command, Args: args}, nil
}

func (d *claudeDriver) ParseStageOutput(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error) {
	return d.parseStageOutputWithEnvelope(spec, capture)
}

// ---------------------------------------------------------------------------
// Codex driver
// ---------------------------------------------------------------------------

type codexDriver struct{ baseDriver }

func newCodexDriver() AgentCLIDriver {
	return &codexDriver{baseDriver{name: loader.AgentDriverCodex}}
}

func (d *codexDriver) BuildInvocation(profile loader.AgentDef, spec *rt.StageSpec, session *rt.RunSession, prompt string) (*CLIInvocation, error) {
	args := append([]string{}, profile.Args...)
	args = append(args, "-q", prompt)
	return &CLIInvocation{Command: profile.Command, Args: args}, nil
}

func (d *codexDriver) ParseStageOutput(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error) {
	return d.parseStageOutputWithEnvelope(spec, capture)
}

// ---------------------------------------------------------------------------
// Qwen driver
// ---------------------------------------------------------------------------

type qwenDriver struct{ baseDriver }

func newQwenDriver() AgentCLIDriver {
	return &qwenDriver{baseDriver{name: loader.AgentDriverQwen}}
}

func (d *qwenDriver) BuildInvocation(profile loader.AgentDef, spec *rt.StageSpec, session *rt.RunSession, prompt string) (*CLIInvocation, error) {
	args := normalizeQwenArgs(profile.Args)
	if session.DriverState.ExternalSessionID != "" {
		args = append(args, "--resume", session.DriverState.ExternalSessionID)
	} else if len(session.StageHistory) > 0 {
		args = append(args, "--continue")
	}
	args = append(args, "-p", prompt, "--output-format", "stream-json", "--yolo")
	return &CLIInvocation{
		Command:           profile.Command,
		Args:              args,
		StructuredLogName: "qwen.response.jsonl",
	}, nil
}

func (d *qwenDriver) ParseStageOutput(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error) {
	text, sessionID, structuredLogName, structuredLog, meta, err := parseQwenStructuredOutput(capture.Stdout)
	if err != nil {
		return nil, err
	}
	env, err := d.parseEnvelopeFromText(text)
	if err != nil {
		return nil, explainQwenParseFailure(meta, err)
	}
	out := d.parsedOutputFromEnvelope(spec.Type, env)
	out.ExternalSessionID = sessionID
	out.StructuredLogName = structuredLogName
	out.StructuredLog = structuredLog
	return out, nil
}

// ---------------------------------------------------------------------------
// Generic driver — universal driver for any CLI agent
// ---------------------------------------------------------------------------

type genericDriver struct{ baseDriver }

func newGenericDriver() AgentCLIDriver {
	return &genericDriver{baseDriver{name: loader.AgentDriverGeneric}}
}

func (d *genericDriver) BuildInvocation(profile loader.AgentDef, spec *rt.StageSpec, session *rt.RunSession, prompt string) (*CLIInvocation, error) {
	args := append([]string{}, profile.Args...)
	args = append(args, "-p", prompt)
	return &CLIInvocation{Command: profile.Command, Args: args}, nil
}

func (d *genericDriver) ParseStageOutput(spec *rt.StageSpec, capture *StageCapture) (*ParsedStageOutput, error) {
	return d.parseStageOutputWithEnvelope(spec, capture)
}

// ---------------------------------------------------------------------------
// Qwen output parsing helpers
// ---------------------------------------------------------------------------

// qwenResultMeta captures metadata from qwen's type:"result" JSON element.
type qwenResultMeta struct {
	HasResult bool
	IsError   bool
	Subtype   string
}

func normalizeQwenArgs(args []string) []string {
	normalized := make([]string, 0, len(args))
	skipNext := false
	for i := 0; i < len(args); i++ {
		if skipNext {
			skipNext = false
			continue
		}

		switch arg := args[i]; {
		case arg == "--output-format":
			if i+1 < len(args) {
				skipNext = true
			}
			continue
		case strings.HasPrefix(arg, "--output-format="):
			continue
		case arg == "--yolo":
			continue
		default:
			normalized = append(normalized, args[i])
		}
	}
	return normalized
}

func explainQwenParseFailure(meta qwenResultMeta, err error) error {
	message := "qwen output is not machine-readable; ensure non-interactive runs use \"qwen -p ... --output-format json --yolo\" and that the configured CLI supports structured headless output"
	if meta.IsError {
		if meta.Subtype != "" {
			return fmt.Errorf("%s (qwen result subtype: %s): %w", message, meta.Subtype, err)
		}
		return fmt.Errorf("%s (qwen reported an error result): %w", message, err)
	}
	if meta.HasResult && meta.Subtype != "" {
		return fmt.Errorf("%s (qwen result subtype: %s): %w", message, meta.Subtype, err)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func parseQwenStructuredOutput(stdout string) (text string, sessionID string, logName string, log []byte, meta qwenResultMeta, err error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return "", "", "", nil, meta, fmt.Errorf("qwen produced empty stdout")
	}

	if strings.HasPrefix(trimmed, "[") {
		text, sessionID, meta, err = parseQwenJSONArray(trimmed)
		if err != nil {
			return "", "", "", nil, meta, err
		}
		return text, sessionID, "qwen.response.json", []byte(trimmed), meta, nil
	}

	if strings.HasPrefix(trimmed, "{") {
		text, sessionID, meta, err = parseQwenJSONLines(trimmed)
		if err == nil {
			return text, sessionID, "qwen.response.jsonl", []byte(trimmed), meta, nil
		}
	}

	preview := trimmed
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return "", "", "", nil, meta, fmt.Errorf("qwen output is not valid JSON and cannot be parsed: %s", preview)
}

func parseQwenJSONArray(data string) (string, string, qwenResultMeta, error) {
	var items []map[string]any
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return "", "", qwenResultMeta{}, fmt.Errorf("parsing qwen json output: %w", err)
	}
	if len(items) == 0 {
		return "", "", qwenResultMeta{}, fmt.Errorf("qwen json output is an empty array")
	}
	return collectQwenText(items)
}

func parseQwenJSONLines(data string) (string, string, qwenResultMeta, error) {
	lines := strings.Split(data, "\n")
	items := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return "", "", qwenResultMeta{}, fmt.Errorf("parsing qwen stream-json output: %w", err)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return "", "", qwenResultMeta{}, fmt.Errorf("qwen jsonl output is empty")
	}
	return collectQwenText(items)
}

func collectQwenText(items []map[string]any) (string, string, qwenResultMeta, error) {
	var parts []string
	sessionID := ""
	meta := qwenResultMeta{}
	for _, item := range items {
		if sessionID == "" {
			if s, _ := item["session_id"].(string); s != "" {
				sessionID = s
			}
		}
		switch item["type"] {
		case "assistant":
			msg, ok := item["message"].(map[string]any)
			if !ok {
				continue
			}
			content, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if blockMap["type"] == "text" {
					if text, _ := blockMap["text"].(string); text != "" {
						parts = append(parts, text)
					}
				}
			}
		case "result":
			meta.HasResult = true
			if isErr, _ := item["is_error"].(bool); isErr {
				meta.IsError = true
			}
			if sub, _ := item["subtype"].(string); sub != "" {
				meta.Subtype = sub
			}
			if text, _ := item["result"].(string); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		return "", sessionID, meta, fmt.Errorf("qwen output did not contain assistant or result text")
	}
	return strings.Join(parts, "\n"), sessionID, meta, nil
}

// ---------------------------------------------------------------------------
// Envelope extraction
// ---------------------------------------------------------------------------

func extractResultEnvelope(text string) ([]byte, error) {
	// Try marker-based extraction first.
	start := strings.Index(text, resultBeginMarker)
	end := strings.Index(text, resultEndMarker)
	if start >= 0 && end > start {
		payload := strings.TrimSpace(text[start+len(resultBeginMarker) : end])
		if payload == "" {
			return nil, fmt.Errorf("empty structured result payload")
		}
		return []byte(payload), nil
	}

	// Fallback: find the last JSON object in the text.
	lastBrace := strings.LastIndex(text, "}")
	if lastBrace >= 0 {
		// Search backwards for the matching opening brace.
		depth := 0
		for i := lastBrace; i >= 0; i-- {
			switch text[i] {
			case '}':
				depth++
			case '{':
				depth--
				if depth == 0 {
					candidate := strings.TrimSpace(text[i : lastBrace+1])
					var test map[string]any
					if json.Unmarshal([]byte(candidate), &test) == nil {
						return []byte(candidate), nil
					}
				}
			}
		}
	}

	preview := text
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return nil, fmt.Errorf("structured CLI result markers not found in output: %s", preview)
}

// ---------------------------------------------------------------------------
// Environment helpers
// ---------------------------------------------------------------------------

func buildStageEnv(spec *rt.StageSpec, profile loader.AgentDef) []string {
	env := []string{
		"AGENTCTL_TASK_ID=" + spec.TaskID,
		"AGENTCTL_RUN_ID=" + spec.RunID,
		"AGENTCTL_SESSION_ID=" + spec.SessionID,
		"AGENTCTL_STAGE_ID=" + spec.StageID,
		"AGENTCTL_STAGE_TYPE=" + string(spec.Type),
		"AGENTCTL_AGENT_ID=" + spec.AgentID,
		"AGENTCTL_WORK_DIR=" + spec.WorkDir,
		"AGENTCTL_SESSION_DIR=" + spec.SessionDir,
		"AGENTCTL_STAGE_DIR=" + spec.StageDir,
		"AGENTCTL_TASK_PATH=" + spec.TaskPath,
		"AGENTCTL_CONTEXT_DIR=" + spec.ContextDir,
		"AGENTCTL_PROMPT_PATH=" + spec.PromptPath,
	}
	for key, value := range profile.Env {
		env = append(env, key+"="+value)
	}
	return env
}
