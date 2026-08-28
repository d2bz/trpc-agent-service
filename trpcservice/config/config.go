// Package config loads and bootstraps platform configuration.
package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

const (
	DemoTenantID   = "demo"
	DemoAgentAppID = "echo"
	DemoRevisionID = "echo-v1"
	// DemoPrincipalID is the only chat principal the local demo credential
	// authenticates as. Sessions are keyed by it, never by a request body.
	DemoPrincipalID = "demo-user"
)

// SeedDemo creates an idempotent local configuration with no external API key.
func SeedDemo(ctx context.Context, repository tenant.Repository) error {
	if repository == nil {
		return fmt.Errorf("config: tenant repository is required")
	}
	_, err := repository.CreateTenant(ctx, tenant.Tenant{
		ID: DemoTenantID, Slug: DemoTenantID, Name: "Demo Tenant",
	})
	if err != nil && !errors.Is(err, tenant.ErrAlreadyExists) {
		return fmt.Errorf("config: create demo tenant: %w", err)
	}
	scope := tenant.TenantContext{TenantID: DemoTenantID}
	_, err = repository.CreateAgentApp(ctx, scope, tenant.AgentApp{
		ID: DemoAgentAppID, TenantID: DemoTenantID, Name: "Echo Assistant",
	})
	if err != nil && !errors.Is(err, tenant.ErrAlreadyExists) {
		return fmt.Errorf("config: create demo app: %w", err)
	}
	_, err = repository.CreateRevision(ctx, scope, tenant.AgentRevision{
		ID:         DemoRevisionID,
		TenantID:   DemoTenantID,
		AgentAppID: DemoAgentAppID,
		RevisionNo: 1,
		CreatedBy:  "system-bootstrap",
		Config: tenant.RevisionConfig{
			AgentName:   "echo-assistant",
			Description: "Deterministic bootstrap agent",
			Instruction: "Return the model response. This bootstrap runtime verifies the service path.",
			Model: tenant.ModelConfig{
				Provider: "deterministic",
				Name:     "deterministic-echo",
			},
		},
	})
	if err != nil && !errors.Is(err, tenant.ErrAlreadyExists) {
		return fmt.Errorf("config: create demo revision: %w", err)
	}
	if _, _, err = repository.PublishRevision(
		ctx, scope, DemoAgentAppID, DemoRevisionID,
	); err != nil {
		return fmt.Errorf("config: publish demo revision: %w", err)
	}
	return nil
}
