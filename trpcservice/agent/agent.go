// Package agent builds and owns tRPC-Agent-Go agent runtimes.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tool"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	openaiserver "trpc.group/trpc-go/trpc-agent-go/server/openai"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const (
	DemoAppName   = "t/demo/a/echo"
	DemoAgentName = "echo-assistant"
	DemoModelName = "deterministic-echo"
)

// maxToolIterations bounds how many rounds of tool calls one run may take. An
// iteration is one assistant response that contains tool calls, so the bound is
// on model-tool-model loops, not on the number of tools called.
//
// It exists because a run holds a session lease for as long as it lasts. A
// model that keeps re-issuing tool calls — a loop it cannot see, a tool error
// it retries forever — would hold that lease and the upstream connection for as
// long as the model kept talking, and no other turn in that conversation could
// proceed. The framework's default is unbounded, which is not a safe default
// for a shared multi-tenant runtime.
//
// Four is conservative on purpose. The registered tools answer in one round; a
// model that calls both, then retries once after a bad-argument error, needs
// three. Exceeding the bound ends the run with an error rather than truncating
// the answer silently, so this is a backstop, not a budget to spend.
const maxToolIterations = 4

type runtimeHTTPAdapter interface {
	Handler() http.Handler
	Close() error
}

// Runtime owns the process-local Agent, Runner and protocol adapter objects for
// one immutable agent revision. Conversation state remains in SessionService.
// Runner is the real Runner and the only one Close releases; protocol adapters
// receive a wrapper that supplies the authenticated identity instead.
type Runtime struct {
	TenantID       string
	AgentAppID     string
	RevisionID     string
	AppName        string
	ModelName      string
	Agent          trpcagent.Agent
	Runner         runner.Runner
	SessionService session.Service

	openAI             runtimeHTTPAdapter
	openAIHandler      http.Handler
	protocolRunner     runner.Runner
	closeOnce          sync.Once
	closeErr           error
	ownsSessionService bool
}

// NewDemoRuntime builds a real tRPC-Agent-Go LLMAgent and Runner without
// requiring an external model API. It stays on the deterministic provider so
// the bootstrap path needs no endpoint and no credential; a revision selects a
// real model through its own ModelConfig.
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
				Provider: ProviderDeterministic,
				Name:     DemoModelName,
			},
		},
	}
	runtime, err := newRuntimeFromRevision(revision, sessionService, true, nil)
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
	return newRuntimeFromRevision(revision, sessionService, false, nil)
}

// newRuntimeFromRevision builds a Runtime. A nil auditSink selects the process
// default, which is slog; it is a parameter so the tool trail can be read back
// without redirecting the logger of the whole process.
func newRuntimeFromRevision(
	revision tenant.AgentRevision,
	sessionService session.Service,
	ownsSessionService bool,
	auditSink tool.AuditSink,
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
	if sessionService == nil {
		return nil, fmt.Errorf("agent: session service is required")
	}
	// Tools and policies are resolved before anything is constructed. A
	// revision the platform cannot authorize must not reach a model, a Runner
	// or a protocol adapter — and running this first also means an unbuildable
	// revision never causes a credential to be resolved.
	tools, err := tool.Builtin().Resolve(revision.Config.ToolRefs, revision.Config.PolicyRefs)
	if err != nil {
		return nil, fmt.Errorf("agent: assemble tools for revision %q: %w", revision.ID, err)
	}
	llmModel, err := newModel(revision.Config.Model)
	if err != nil {
		return nil, fmt.Errorf("agent: build model for revision %q: %w", revision.ID, err)
	}
	appName := fmt.Sprintf("t/%s/a/%s", revision.TenantID, revision.AgentAppID)
	options := []llmagent.Option{
		llmagent.WithModel(llmModel),
		llmagent.WithDescription(revision.Config.Description),
		llmagent.WithInstruction(revision.Config.Instruction),
		llmagent.WithGenerationConfig(generationConfig(revision.Config.Model)),
	}
	// A revision with no tools is built exactly as before. The tool options are
	// not merely inert without tools — the iteration bound and the callbacks
	// change agent behavior — so a revision that never asked for tools does not
	// get them.
	if len(tools) > 0 {
		options = append(
			options,
			llmagent.WithTools(tools),
			llmagent.WithMaxToolIterations(maxToolIterations),
			llmagent.WithToolCallbacks(tool.NewAuditCallbacks(auditSink)),
		)
	}
	ag := llmagent.New(revision.Config.AgentName, options...)
	r := runner.NewRunner(
		appName,
		ag,
		runner.WithSessionService(sessionService),
	)
	runtime := &Runtime{
		TenantID:           revision.TenantID,
		AgentAppID:         revision.AgentAppID,
		RevisionID:         revision.ID,
		AppName:            appName,
		ModelName:          revision.Config.Model.Name,
		Agent:              ag,
		Runner:             r,
		SessionService:     sessionService,
		ownsSessionService: ownsSessionService,
	}
	// The adapter reads userID and sessionID from the request payload, so it
	// receives the identity-enforcing wrapper instead of the real Runner.
	runtime.protocolRunner = &contextRunner{
		inner:      runtime.Runner,
		tenantID:   revision.TenantID,
		appID:      revision.AgentAppID,
		revisionID: revision.ID,
	}
	openAI, err := openaiserver.New(
		openaiserver.WithRunner(runtime.protocolRunner),
		openaiserver.WithSessionService(runtime.SessionService),
		openaiserver.WithAppName(runtime.AppName),
		openaiserver.WithModelName(runtime.ModelName),
		openaiserver.WithBasePath("/v1"),
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("agent: create OpenAI adapter for revision %q: %w", revision.ID, err),
			runtime.Close(),
		)
	}
	runtime.openAI = openAI
	runtime.openAIHandler = openAI.Handler()
	if runtime.openAIHandler == nil {
		return nil, errors.Join(
			fmt.Errorf("agent: OpenAI adapter for revision %q has no handler", revision.ID),
			runtime.Close(),
		)
	}
	return runtime, nil
}

// OpenAIHandler returns the single protocol handler owned by this Runtime. It
// is resolved once at build time, so callers must not cache adapters per
// Runtime pointer.
func (r *Runtime) OpenAIHandler() (http.Handler, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	return r.openAIHandler, nil
}

func (r *Runtime) validate() error {
	if r == nil {
		return fmt.Errorf("agent: runtime is nil")
	}
	if r.TenantID == "" || r.AgentAppID == "" || r.RevisionID == "" {
		return fmt.Errorf("agent: runtime identity is incomplete")
	}
	if r.AppName == "" || r.ModelName == "" || r.Agent == nil || r.Runner == nil ||
		r.SessionService == nil || r.openAI == nil || r.openAIHandler == nil {
		return fmt.Errorf("agent: runtime execution unit is incomplete")
	}
	return nil
}

func (r *Runtime) validateFor(revision tenant.AgentRevision) error {
	if err := r.validate(); err != nil {
		return err
	}
	if r.TenantID != revision.TenantID || r.AgentAppID != revision.AgentAppID ||
		r.RevisionID != revision.ID {
		return fmt.Errorf(
			"agent: runtime identity %q/%q/%q does not match revision %q/%q/%q",
			r.TenantID,
			r.AgentAppID,
			r.RevisionID,
			revision.TenantID,
			revision.AgentAppID,
			revision.ID,
		)
	}
	return nil
}

// Close releases the protocol adapter, the Runner, and any Session service
// owned by this Runtime, in that order. It is safe for concurrent use and
// idempotent: every call returns the error produced by the first close.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var closeErr error
		if r.openAI != nil {
			closeErr = errors.Join(closeErr, r.openAI.Close())
		}
		if r.Runner != nil {
			closeErr = errors.Join(closeErr, r.Runner.Close())
		}
		if r.ownsSessionService && r.SessionService != nil {
			closeErr = errors.Join(closeErr, r.SessionService.Close())
		}
		r.closeErr = closeErr
	})
	return r.closeErr
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
