package config

import (
	"context"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
)

func TestSeedDemoIsIdempotentAndPublished(t *testing.T) {
	repository := tenant.NewMemoryRepository()
	require.NoError(t, SeedDemo(context.Background(), repository))
	require.NoError(t, SeedDemo(context.Background(), repository))

	revision, err := repository.ResolveRevision(
		context.Background(),
		tenant.TenantContext{TenantID: DemoTenantID},
		DemoAgentAppID,
		"",
	)
	require.NoError(t, err)
	require.Equal(t, DemoRevisionID, revision.ID)
	require.Equal(t, tenant.RevisionStatusPublished, revision.Status)
	require.Equal(t, "deterministic", revision.Config.Model.Provider)
}
