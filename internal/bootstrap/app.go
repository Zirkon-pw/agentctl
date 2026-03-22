package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/docup/agentctl/internal/app/command"
	"github.com/docup/agentctl/internal/app/query"
	"github.com/docup/agentctl/internal/infra/events"
	"github.com/docup/agentctl/internal/infra/fsstore"
	infrart "github.com/docup/agentctl/internal/infra/runtime"
	"github.com/docup/agentctl/internal/service/clarificationflow"
	"github.com/docup/agentctl/internal/service/contextpack"
	"github.com/docup/agentctl/internal/service/prompting"
	"github.com/docup/agentctl/internal/service/runtimecontrol"
	"github.com/docup/agentctl/internal/service/taskrunner"
	"github.com/docup/agentctl/internal/service/workspace"
)

// App holds all wired services and command/query handlers.
type App struct {
	// Stores
	TaskStore     *fsstore.TaskStore
	RunStore      *fsstore.RunStore
	ClarStore     *fsstore.ClarificationStore
	TemplateStore *fsstore.TemplateStore

	// Services
	Orchestrator *taskrunner.Orchestrator
	ClarMgr      *clarificationflow.Manager
	RuntimeMgr   *runtimecontrol.Manager

	// Commands
	CreateTask *command.CreateTask
	UpdateTask *command.UpdateTask
	RunTask    *command.RunTask

	// Queries
	ListTasks   *query.ListTasks
	InspectTask *query.InspectTask

	// Config
	AgentctlDir string
	ProjectRoot string
}

// NewApp creates a fully wired application from the workspace.
func NewApp() (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	ws, err := workspace.Load(cwd)
	if err != nil {
		return nil, err
	}

	agentctlDir := ws.Workspace.AgentctlDir
	projectRoot := ws.Workspace.Root

	// Stores
	taskStore := fsstore.NewTaskStore(agentctlDir)
	runStore := fsstore.NewRunStore(agentctlDir)
	clarStore := fsstore.NewClarificationStore(agentctlDir)
	templateStore := fsstore.NewTemplateStore(agentctlDir)

	// Infrastructure
	registry := infrart.NewRegistry(agentctlDir)
	heartbeatMgr := infrart.NewHeartbeatManager(agentctlDir)
	eventSink := events.NewSink(filepath.Join(agentctlDir, "runtime"))
	adapterRegistry := taskrunner.NewAgentAdapterRegistry(ws.Agents)

	// Services
	ctxBuilder := contextpack.NewBuilder(agentctlDir, projectRoot)
	promptBuilder := prompting.NewBuilder(templateStore, agentctlDir)
	supervisor := taskrunner.NewTaskSupervisor(
		taskStore, runStore, clarStore, registry, heartbeatMgr, eventSink,
		ctxBuilder, promptBuilder, ws.Config, adapterRegistry, projectRoot,
	)

	orchestrator := taskrunner.NewOrchestrator(
		taskStore, runStore, registry, eventSink, ws.Config, supervisor,
	)

	clarMgr := clarificationflow.NewManager(taskStore, clarStore)
	rtMgr := runtimecontrol.NewManager(registry, heartbeatMgr, eventSink, ws.Config.Runtime.StaleAfterSec)

	// Commands
	createTask := command.NewCreateTask(taskStore, ws.Config)
	updateTask := command.NewUpdateTask(taskStore)
	runTask := command.NewRunTask(orchestrator)

	// Queries
	listTasks := query.NewListTasks(taskStore)
	inspectTask := query.NewInspectTask(taskStore)

	return &App{
		TaskStore:     taskStore,
		RunStore:      runStore,
		ClarStore:     clarStore,
		TemplateStore: templateStore,
		Orchestrator:  orchestrator,
		ClarMgr:       clarMgr,
		RuntimeMgr:    rtMgr,
		CreateTask:    createTask,
		UpdateTask:    updateTask,
		RunTask:       runTask,
		ListTasks:     listTasks,
		InspectTask:   inspectTask,
		AgentctlDir:   agentctlDir,
		ProjectRoot:   projectRoot,
	}, nil
}
