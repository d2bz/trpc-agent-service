package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryRepositoryPublishResolveAndRevisionImmutability(t *testing.T) {
	repository := NewMemoryRepository()
	scope := TenantContext{TenantID: "tenant-a"}
	createTenantAndApp(t, repository, scope, "assistant")

	temperature := 0.4
	revisionInput := AgentRevision{
		ID:         "revision-1",
		TenantID:   scope.TenantID,
		AgentAppID: "assistant",
		RevisionNo: 1,
		CreatedBy:  "user-admin",
		Config: RevisionConfig{
			AgentName:   "support-agent",
			Instruction: "Help the user.",
			Model: ModelConfig{
				Provider:    "deterministic",
				Name:        "echo-v1",
				Temperature: &temperature,
			},
			ToolRefs: []string{"orders.read"},
		},
	}
	created, err := repository.CreateRevision(context.Background(), scope, revisionInput)
	require.NoError(t, err)
	require.Len(t, created.ConfigDigest, 64)
	require.Equal(t, RevisionStatusDraft, created.Status)

	_, err = repository.ResolveRevision(context.Background(), scope, "assistant", "")
	require.ErrorIs(t, err, ErrNoPublishedRevision)

	revisionInput.Config.ToolRefs[0] = "orders.delete"
	temperature = 1.8
	stored, err := repository.GetRevision(context.Background(), scope, "assistant", "revision-1")
	require.NoError(t, err)
	require.Equal(t, []string{"orders.read"}, stored.Config.ToolRefs)
	require.Equal(t, 0.4, *stored.Config.Model.Temperature)

	stored.Config.ToolRefs[0] = "mutated.after.read"
	*stored.Config.Model.Temperature = 2
	storedAgain, err := repository.GetRevision(context.Background(), scope, "assistant", "revision-1")
	require.NoError(t, err)
	require.Equal(t, []string{"orders.read"}, storedAgain.Config.ToolRefs)
	require.Equal(t, 0.4, *storedAgain.Config.Model.Temperature)

	app, published, err := repository.PublishRevision(
		context.Background(), scope, "assistant", "revision-1",
	)
	require.NoError(t, err)
	require.Equal(t, RevisionStatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	require.Equal(t, uint64(1), app.RoutingVersion)
	require.Equal(t, "revision-1", app.RoutingPolicy.DefaultRevisionID)
	require.Equal(t, uint32(10000), app.RoutingPolicy.Routes[0].Weight)

	resolved, err := repository.ResolveRevision(context.Background(), scope, "assistant", "")
	require.NoError(t, err)
	require.Equal(t, "revision-1", resolved.ID)

	app, _, err = repository.PublishRevision(
		context.Background(), scope, "assistant", "revision-1",
	)
	require.NoError(t, err)
	require.Equal(t, uint64(1), app.RoutingVersion, "idempotent publish must not advance routing")
}

func TestMemoryRepositoryDefaultAndPinnedRevisionRouting(t *testing.T) {
	repository := NewMemoryRepository()
	scope := TenantContext{TenantID: "tenant-a"}
	createTenantAndApp(t, repository, scope, "assistant")
	createAndPublishRevision(t, repository, scope, "assistant", "revision-1", 1)
	createAndPublishRevision(t, repository, scope, "assistant", "revision-2", 2)

	defaultRevision, err := repository.ResolveRevision(context.Background(), scope, "assistant", "")
	require.NoError(t, err)
	require.Equal(t, "revision-2", defaultRevision.ID)

	pinnedRevision, err := repository.ResolveRevision(
		context.Background(), scope, "assistant", "revision-1",
	)
	require.NoError(t, err)
	require.Equal(t, "revision-1", pinnedRevision.ID)

	app, rolledBackRevision, err := repository.PublishRevision(
		context.Background(), scope, "assistant", "revision-1",
	)
	require.NoError(t, err)
	require.Equal(t, "revision-1", rolledBackRevision.ID)
	require.Equal(t, "revision-1", app.RoutingPolicy.DefaultRevisionID)
	require.Equal(t, uint64(3), app.RoutingVersion)

	rolledBackDefault, err := repository.ResolveRevision(
		context.Background(), scope, "assistant", "",
	)
	require.NoError(t, err)
	require.Equal(t, "revision-1", rolledBackDefault.ID)
}

func TestMemoryRepositoryEnforcesTenantIsolation(t *testing.T) {
	repository := NewMemoryRepository()
	tenantA := TenantContext{TenantID: "tenant-a"}
	tenantB := TenantContext{TenantID: "tenant-b"}
	createTenantAndApp(t, repository, tenantA, "shared-app-id")
	createTenantAndApp(t, repository, tenantB, "shared-app-id")
	createAndPublishRevision(t, repository, tenantA, "shared-app-id", "revision-1", 1)
	createAndPublishRevision(t, repository, tenantB, "shared-app-id", "revision-1", 1)

	revisionA, err := repository.ResolveRevision(
		context.Background(), tenantA, "shared-app-id", "",
	)
	require.NoError(t, err)
	revisionB, err := repository.ResolveRevision(
		context.Background(), tenantB, "shared-app-id", "",
	)
	require.NoError(t, err)
	require.Equal(t, tenantA.TenantID, revisionA.TenantID)
	require.Equal(t, tenantB.TenantID, revisionB.TenantID)

	_, err = repository.CreateAgentApp(context.Background(), tenantA, AgentApp{
		ID:       "wrong-scope",
		TenantID: tenantB.TenantID,
		Name:     "Wrong Scope",
	})
	require.ErrorIs(t, err, ErrTenantScope)

	_, err = repository.GetRevision(
		context.Background(), tenantB, "missing-app", "revision-1",
	)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryRepositoryRejectsInvalidAndDuplicateConfiguration(t *testing.T) {
	repository := NewMemoryRepository()
	_, err := repository.CreateTenant(context.Background(), Tenant{
		ID:   "tenant/unsafe",
		Slug: "unsafe",
		Name: "Unsafe",
	})
	require.ErrorIs(t, err, ErrInvalidArgument)

	createTenant(t, repository, "tenant-a", StatusActive)
	_, err = repository.CreateTenant(context.Background(), Tenant{
		ID:   "tenant-b",
		Slug: "tenant-a",
		Name: "Duplicate Slug",
	})
	require.ErrorIs(t, err, ErrAlreadyExists)

	suspendedScope := TenantContext{TenantID: "tenant-suspended"}
	createTenant(t, repository, suspendedScope.TenantID, StatusSuspended)
	_, err = repository.CreateAgentApp(context.Background(), suspendedScope, AgentApp{
		ID:       "assistant",
		TenantID: suspendedScope.TenantID,
		Name:     "Assistant",
	})
	require.ErrorIs(t, err, ErrTenantInactive)

	scope := TenantContext{TenantID: "tenant-a"}
	_, err = repository.CreateAgentApp(context.Background(), scope, AgentApp{
		ID:       "assistant",
		TenantID: scope.TenantID,
		Name:     "Assistant",
		RoutingPolicy: RoutingPolicy{
			DefaultRevisionID: "caller-controlled",
		},
		RoutingVersion: 99,
	})
	require.NoError(t, err)
	storedApp, err := repository.GetAgentApp(context.Background(), scope, "assistant")
	require.NoError(t, err)
	require.Zero(t, storedApp.RoutingVersion)
	require.Empty(t, storedApp.RoutingPolicy.DefaultRevisionID)

	createAndPublishRevision(t, repository, scope, "assistant", "revision-1", 1)
	_, err = repository.CreateRevision(context.Background(), scope, AgentRevision{
		ID:         "revision-other",
		TenantID:   scope.TenantID,
		AgentAppID: "assistant",
		RevisionNo: 1,
		CreatedBy:  "test-admin",
		Config: RevisionConfig{
			AgentName: "duplicate-number",
			Model:     ModelConfig{Provider: "deterministic", Name: "echo-test"},
		},
	})
	require.ErrorIs(t, err, ErrAlreadyExists)

	_, err = repository.CreateRevision(context.Background(), scope, AgentRevision{
		ID:         "revision-draft",
		TenantID:   scope.TenantID,
		AgentAppID: "assistant",
		RevisionNo: 2,
		CreatedBy:  "test-admin",
		Config: RevisionConfig{
			AgentName: "draft-agent",
			Model:     ModelConfig{Provider: "deterministic", Name: "echo-test"},
		},
	})
	require.NoError(t, err)
	_, err = repository.ResolveRevision(
		context.Background(), scope, "assistant", "revision-draft",
	)
	require.ErrorIs(t, err, ErrRevisionNotPublished)
}

func TestMemoryRepositoryHonorsCanceledContext(t *testing.T) {
	repository := NewMemoryRepository()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repository.GetTenant(ctx, "tenant-a")
	require.True(t, errors.Is(err, context.Canceled))
}

func createTenantAndApp(
	t *testing.T,
	repository *MemoryRepository,
	scope TenantContext,
	appID string,
) {
	t.Helper()
	createTenant(t, repository, scope.TenantID, StatusActive)
	_, err := repository.CreateAgentApp(context.Background(), scope, AgentApp{
		ID:       appID,
		TenantID: scope.TenantID,
		Name:     "Test App",
	})
	require.NoError(t, err)
}

func createTenant(t *testing.T, repository *MemoryRepository, tenantID string, status Status) {
	t.Helper()
	_, err := repository.CreateTenant(context.Background(), Tenant{
		ID:     tenantID,
		Slug:   tenantID,
		Name:   "Test Tenant " + tenantID,
		Status: status,
	})
	require.NoError(t, err)
}

func createAndPublishRevision(
	t *testing.T,
	repository *MemoryRepository,
	scope TenantContext,
	appID string,
	revisionID string,
	revisionNo uint64,
) AgentRevision {
	t.Helper()
	created, err := repository.CreateRevision(context.Background(), scope, AgentRevision{
		ID:         revisionID,
		TenantID:   scope.TenantID,
		AgentAppID: appID,
		RevisionNo: revisionNo,
		CreatedBy:  "test-admin",
		Config: RevisionConfig{
			AgentName: "test-agent",
			Model: ModelConfig{
				Provider: "deterministic",
				Name:     "echo-test",
			},
		},
	})
	require.NoError(t, err)
	_, published, err := repository.PublishRevision(
		context.Background(), scope, appID, created.ID,
	)
	require.NoError(t, err)
	return published
}
