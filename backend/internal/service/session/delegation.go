package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/domain"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/httpd/apierr"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/ports"
	sessionmanager "github.com/sudo-adduser-jordan/open-agents/backend/internal/session_manager"
)

const (
	delegatedTaskTitleLimit             = 20
	delegatedTaskUntitledName           = "Untitled task"
	delegatedTaskTitleRefinementTimeout = time.Minute
)

// DelegateTaskInput describes a task Open Agents should spawn as a worker session. Brief
// may be empty to open an idle worker that the user can instruct later. Empty
// RequestedAgent means the spawn uses the project's worker-agent default.
type DelegateTaskInput struct {
	ProjectID      domain.ProjectID
	Brief          string
	RequestedAgent domain.AgentHarness
	Model          string
	ApprovalMode   domain.PermissionMode
	RequestedMode  domain.SessionMode
	Attachments    []ports.SpawnAttachment
}

// DelegateTaskOutcome identifies the spawned worker. ManagerID remains
// optional for wire compatibility; asynchronous title refinement does not wait
// to resolve the manager before returning.
type DelegateTaskOutcome struct {
	ManagerID domain.SessionID
	WorkerID  domain.SessionID
}

// DelegateTask spawns the worker directly, matching `open-agents spawn`, with a
// provisional display name derived from the task brief. Open Agents then best-effort
// refines that title in the background through the project manager,
// resuming or creating the manager when necessary.
func (s *Service) DelegateTask(ctx context.Context, in DelegateTaskInput) (DelegateTaskOutcome, error) {
	if _, err := s.requireProject(ctx, in.ProjectID); err != nil {
		return DelegateTaskOutcome{}, err
	}
	if in.RequestedAgent != "" && !in.RequestedAgent.IsKnown() {
		return DelegateTaskOutcome{}, apierr.Invalid("UNKNOWN_HARNESS", "Unknown requested agent", nil)
	}
	if in.RequestedMode != "" && !in.RequestedMode.Valid() {
		return DelegateTaskOutcome{}, apierr.Invalid("INVALID_SESSION_MODE", "mode must be chat or tui", nil)
	}
	prompt := in.Brief
	if strings.TrimSpace(prompt) == "" {
		prompt = ""
	}

	worker, _, _, err := s.manager.Spawn(ctx, ports.SpawnConfig{
		ProjectID:             in.ProjectID,
		Kind:                  domain.KindWorker,
		RequestedWorkflowMode: domain.WorkflowModePlanning,
		Harness:               in.RequestedAgent,
		Prompt:                prompt,
		DisplayName:           delegatedTaskDisplayName(in.Brief),
		AgentConfig: ports.AgentConfig{
			Model:       strings.TrimSpace(in.Model),
			Permissions: in.ApprovalMode,
		},
		RequestedMode: in.RequestedMode,
		Attachments:   in.Attachments,
	})
	if err != nil {
		return DelegateTaskOutcome{}, toSpawnAPIError(err)
	}

	// The worker spawn is the commit point. Manager startup and title
	// generation must never hold the new-task response open. A promptless worker
	// stays idle with its provisional title until the user supplies instructions.
	if prompt != "" {
		s.refineDelegatedTaskTitleInBackground(worker.ID, in)
	}
	return DelegateTaskOutcome{WorkerID: worker.ID}, nil
}

func (s *Service) refineDelegatedTaskTitleInBackground(workerID domain.SessionID, in DelegateTaskInput) {
	work := func() {
		base := s.backgroundContext
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithTimeout(base, delegatedTaskTitleRefinementTimeout)
		defer cancel()

		if err := s.refineDelegatedTaskTitle(ctx, workerID, in); err != nil && s.logger != nil {
			s.logger.Warn("delegated task title refinement failed",
				"projectID", in.ProjectID,
				"workerID", workerID,
				"error", err,
			)
		}
	}
	if s.runBackground != nil {
		s.runBackground(work)
		return
	}
	go work()
}

func (s *Service) refineDelegatedTaskTitle(ctx context.Context, workerID domain.SessionID, in DelegateTaskInput) error {
	managerID, err := s.taskTitleManager(ctx, in.ProjectID)
	if err != nil {
		return err
	}
	if err := s.manager.WaitForMessageDeliveryReady(ctx, managerID); err != nil {
		return fmt.Errorf("wait for title manager %s: %w", managerID, err)
	}
	if err := s.manager.Send(ctx, managerID, taskTitleDelegationMessage(workerID, in), nil); err != nil {
		return fmt.Errorf("send title request to %s: %w", managerID, err)
	}
	return nil
}

func (s *Service) taskTitleManager(ctx context.Context, projectID domain.ProjectID) (domain.SessionID, error) {
	unlock := s.lockManagerProject(projectID)
	managers, err := s.activeManagers(ctx, projectID)
	if err != nil {
		unlock()
		return "", fmt.Errorf("list project managers: %w", err)
	}

	running := make([]domain.Session, 0, len(managers))
	for _, manager := range managers {
		if manager.Activity.State != domain.ActivityExited {
			running = append(running, manager)
		}
	}
	if len(running) > 0 {
		managerID := newestSession(running).ID
		unlock()
		return managerID, nil
	}
	if len(managers) > 0 {
		managerID := newestSession(managers).ID
		_, resumeErr := s.manager.ResumeAgentWithMode(ctx, managerID)
		unlock()
		if resumeErr != nil && !errors.Is(resumeErr, sessionmanager.ErrAgentNotExited) {
			return "", fmt.Errorf("resume project manager %s: %w", managerID, resumeErr)
		}
		return managerID, nil
	}
	unlock()

	manager, err := s.SpawnManager(ctx, projectID, false, "")
	if err != nil {
		return "", fmt.Errorf("start project manager: %w", err)
	}
	return manager.ID, nil
}

func delegatedTaskDisplayName(brief string) string {
	title := strings.Join(strings.Fields(brief), " ")
	if title == "" {
		return delegatedTaskUntitledName
	}
	if utf8.RuneCountInString(title) <= delegatedTaskTitleLimit {
		return title
	}
	return strings.TrimSpace(string([]rune(title)[:delegatedTaskTitleLimit]))
}

func taskTitleDelegationMessage(workerID domain.SessionID, in DelegateTaskInput) string {
	var b strings.Builder
	b.WriteString("Open Agents TASK TITLE UPDATE\n")
	b.WriteString("A worker was already spawned directly with the user's task. Do not spawn another worker or manager, and do not implement the task in this manager session.\n")
	b.WriteString("Choose a concise task title from the brief and run:\n\n")
	b.WriteString("open-agents session rename ")
	b.WriteString(string(workerID))
	b.WriteString(" \"<title, max 20 chars>\"\n\n")
	b.WriteString("Worker session id: ")
	b.WriteString(string(workerID))
	b.WriteString("\nTask brief:\n")
	b.WriteString(in.Brief)
	if model := strings.TrimSpace(in.Model); model != "" {
		b.WriteString("\nRequested model: ")
		b.WriteString(model)
	}
	return b.String()
}
