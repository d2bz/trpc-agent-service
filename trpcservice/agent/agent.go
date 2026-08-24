// Package agent builds and owns tRPC-Agent-Go agent runtimes.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const (
	DemoAppName   = "t/demo/a/echo"
	DemoAgentName = "echo-assistant"
	DemoModelName = "deterministic-echo"
)

// Runtime owns the process-local Agent and Runner objects for one immutable
// agent revision. Conversation state remains in SessionService.
type Runtime struct {
	TenantID       string
	AgentAppID     string
	RevisionID     string
	AppName        string
	ModelName      string
	Agent          trpcagent.Agent
	Runner         runner.Runner
	SessionService session.Service

	closeOnce          sync.Once
	ownsSessionService bool
}

// NewDemoRuntime builds a real tRPC-Agent-Go LLMAgent and Runner without
// requiring an external model API. It is the first executable vertical slice;
// later revisions will replace the deterministic model through configuration.
func NewDemoRuntime() *Runtime {
	sessionService := sessioninmemory.NewSessionService()
	revision := tenant.AgentRevision{
		ID:         "echo-v1",
		TenantID:   "demo",
		AgentAppID: "echo",
		Status:     tenant.RevisionStatusPublished,
		Config: tenant.RevisionConfig{
			AgentName:   DemoAgentName,
			Description: "Deterministic bootstrap agent",
			Instruction: "Return the model response. This bootstrap runtime verifies the service path.",
			Model: tenant.ModelConfig{
				Provider: "deterministic",
				Name:     DemoModelName,
			},
		},
	}
	runtime, err := newRuntimeFromRevision(revision, sessionService, true)
	if err != nil {
		_ = sessionService.Close()
		panic(fmt.Sprintf("agent: build static demo runtime: %v", err))
	}
	return runtime
}

// NewRuntimeFromRevision builds the currently supported runtime from an
// immutable published revision. The caller retains ownership of sessionService.
func NewRuntimeFromRevision(
	revision tenant.AgentRevision,
	sessionService session.Service,
) (*Runtime, error) {
	return newRuntimeFromRevision(revision, sessionService, false)
}

func newRuntimeFromRevision(
	revision tenant.AgentRevision,
	sessionService session.Service,
	ownsSessionService bool,
) (*Runtime, error) {
	if revision.Status != tenant.RevisionStatusPublished {
		return nil, fmt.Errorf("agent: revision %q is not published", revision.ID)
	}
	if err := (tenant.TenantContext{TenantID: revision.TenantID}).Validate(); err != nil {
		return nil, fmt.Errorf("agent: invalid runtime tenant: %w", err)
	}
	if err := tenant.ValidateResourceID("app id", revision.AgentAppID); err != nil {
		return nil, fmt.Errorf("agent: invalid runtime app: %w", err)
	}
	if err := tenant.ValidateResourceID("revision id", revision.ID); err != nil {
		return nil, fmt.Errorf("agent: invalid runtime revision: %w", err)
	}
	if _, err := revision.Config.Digest(); err != nil {
		return nil, fmt.Errorf("agent: invalid revision config: %w", err)
	}
	if revision.Config.Model.Provider != "deterministic" {
		return nil, fmt.Errorf(
			"agent: unsupported bootstrap model provider %q",
			revision.Config.Model.Provider,
		)
	}
	if sessionService == nil {
		return nil, fmt.Errorf("agent: session service is required")
	}
	appName := fmt.Sprintf("t/%s/a/%s", revision.TenantID, revision.AgentAppID)
	ag := llmagent.New(
		revision.Config.AgentName,
		llmagent.WithModel(deterministicModel{name: revision.Config.Model.Name}),
		llmagent.WithDescription(revision.Config.Description),
		llmagent.WithInstruction(revision.Config.Instruction),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
	)
	r := runner.NewRunner(
		appName,
		ag,
		runner.WithSessionService(sessionService),
	)
	return &Runtime{
		TenantID:           revision.TenantID,
		AgentAppID:         revision.AgentAppID,
		RevisionID:         revision.ID,
		AppName:            appName,
		ModelName:          revision.Config.Model.Name,
		Agent:              ag,
		Runner:             r,
		SessionService:     sessionService,
		ownsSessionService: ownsSessionService,
	}, nil
}

// Close releases the Runner and any Session service owned by this Runtime.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.closeOnce.Do(func() {
		if r.Runner != nil {
			closeErr = errors.Join(closeErr, r.Runner.Close())
		}
		if r.ownsSessionService && r.SessionService != nil {
			closeErr = errors.Join(closeErr, r.SessionService.Close())
		}
	})
	return closeErr
}

type deterministicModel struct {
	name string
}

func (m deterministicModel) Info() model.Info {
	return model.Info{Name: m.name, ContextWindow: 4096}
}

func (m deterministicModel) GenerateContent(
	ctx context.Context,
	request *model.Request,
) (<-chan *model.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("deterministic model: nil request")
	}
	lastContent := lastUserContent(request.Messages)
	content := "echo: " + lastContent
	responses := make(chan *model.Response, 3)
	go func() {
		defer close(responses)
		if request.Stream {
			if !sendResponse(ctx, responses, &model.Response{
				Object:    model.ObjectTypeChatCompletionChunk,
				Model:     m.name,
				IsPartial: true,
				Choices: []model.Choice{{
					Index: 0,
					Delta: model.Message{Role: model.RoleAssistant, Content: "echo: "},
				}},
			}) {
				return
			}
			if !sendResponse(ctx, responses, &model.Response{
				Object:    model.ObjectTypeChatCompletionChunk,
				Model:     m.name,
				IsPartial: true,
				Choices: []model.Choice{{
					Index: 0,
					Delta: model.Message{Content: lastContent},
				}},
			}) {
				return
			}
		}
		_ = sendResponse(ctx, responses, &model.Response{
			Object: model.ObjectTypeChatCompletion,
			Model:  m.name,
			Done:   true,
			Choices: []model.Choice{{
				Index:   0,
				Message: model.NewAssistantMessage(content),
			}},
		})
	}()
	return responses, nil
}

func lastUserContent(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == model.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

func sendResponse(
	ctx context.Context,
	responses chan<- *model.Response,
	response *model.Response,
) bool {
	select {
	case <-ctx.Done():
		return false
	case responses <- response:
		return true
	}
}
