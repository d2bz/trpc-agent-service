// Package tenant models multi-tenant isolation and agent configuration.
package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidArgument      = errors.New("tenant: invalid argument")
	ErrTenantScope          = errors.New("tenant: resource is outside tenant scope")
	ErrNotFound             = errors.New("tenant: resource not found")
	ErrAlreadyExists        = errors.New("tenant: resource already exists")
	ErrTenantInactive       = errors.New("tenant: tenant is not active")
	ErrNoPublishedRevision  = errors.New("tenant: no published revision")
	ErrRevisionNotPublished = errors.New("tenant: revision is not published")
)

var (
	slugPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// TenantContext is the mandatory tenant scope for tenant-owned resources.
type TenantContext struct {
	TenantID string
}

// Validate rejects requests that did not resolve an explicit tenant.
func (c TenantContext) Validate() error {
	return ValidateResourceID("tenant_id", c.TenantID)
}

// ValidateResourceID prevents ambiguous session namespaces and cache keys.
func ValidateResourceID(field string, value string) error {
	if !resourceIDPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s", ErrInvalidArgument, field)
	}
	return nil
}

type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleting  Status = "deleting"
)

type Quota struct {
	MaxConcurrentRuns int   `json:"max_concurrent_runs,omitempty"`
	MonthlyTokenLimit int64 `json:"monthly_token_limit,omitempty"`
	MonthlyCostCents  int64 `json:"monthly_cost_cents,omitempty"`
}

type AuditPolicy struct {
	RetainContent bool `json:"retain_content"`
	RetentionDays int  `json:"retention_days,omitempty"`
}

// Tenant is the highest isolation boundary in the platform.
type Tenant struct {
	ID          string      `json:"id"`
	Slug        string      `json:"slug"`
	Name        string      `json:"name"`
	Status      Status      `json:"status"`
	Quota       Quota       `json:"quota"`
	AuditPolicy AuditPolicy `json:"audit_policy"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

func (t Tenant) validate() error {
	if err := ValidateResourceID("tenant id", t.ID); err != nil {
		return err
	}
	if !slugPattern.MatchString(t.Slug) {
		return fmt.Errorf("%w: tenant slug must match %s", ErrInvalidArgument, slugPattern)
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("%w: tenant name is required", ErrInvalidArgument)
	}
	switch t.Status {
	case StatusActive, StatusSuspended, StatusDeleting:
		return nil
	default:
		return fmt.Errorf("%w: unsupported tenant status %q", ErrInvalidArgument, t.Status)
	}
}

type AppStatus string

const (
	AppStatusActive   AppStatus = "active"
	AppStatusDisabled AppStatus = "disabled"
)

// RevisionRoute reserves the routing shape used by future gray releases.
type RevisionRoute struct {
	RevisionID string `json:"revision_id"`
	Weight     uint32 `json:"weight"`
}

type RoutingPolicy struct {
	DefaultRevisionID string          `json:"default_revision_id,omitempty"`
	Routes            []RevisionRoute `json:"routes,omitempty"`
}

// AgentApp is the stable identity whose sessions survive revision changes.
type AgentApp struct {
	ID             string        `json:"id"`
	TenantID       string        `json:"tenant_id"`
	Name           string        `json:"name"`
	Status         AppStatus     `json:"status"`
	RoutingVersion uint64        `json:"routing_version"`
	RoutingPolicy  RoutingPolicy `json:"routing_policy"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

func (a AgentApp) validate(scope TenantContext) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if a.TenantID != scope.TenantID {
		return fmt.Errorf("%w: app tenant %q does not match %q", ErrTenantScope, a.TenantID, scope.TenantID)
	}
	if err := ValidateResourceID("app id", a.ID); err != nil {
		return err
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("%w: app name is required", ErrInvalidArgument)
	}
	switch a.Status {
	case AppStatusActive, AppStatusDisabled:
		return nil
	default:
		return fmt.Errorf("%w: unsupported app status %q", ErrInvalidArgument, a.Status)
	}
}

type RevisionStatus string

const (
	RevisionStatusDraft     RevisionStatus = "draft"
	RevisionStatusPublished RevisionStatus = "published"
	RevisionStatusRetired   RevisionStatus = "retired"
)

type ModelConfig struct {
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	SecretRef   string   `json:"secret_ref,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
}

// RevisionConfig contains references only; secret values are resolved later.
type RevisionConfig struct {
	AgentName        string      `json:"agent_name"`
	Description      string      `json:"description,omitempty"`
	Instruction      string      `json:"instruction,omitempty"`
	Model            ModelConfig `json:"model"`
	ToolRefs         []string    `json:"tool_refs,omitempty"`
	SkillRefs        []string    `json:"skill_refs,omitempty"`
	KnowledgeRefs    []string    `json:"knowledge_refs,omitempty"`
	PolicyRefs       []string    `json:"policy_refs,omitempty"`
	BackendProfileID string      `json:"backend_profile_id,omitempty"`
}

func (c RevisionConfig) validate() error {
	if strings.TrimSpace(c.AgentName) == "" {
		return fmt.Errorf("%w: agent name is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(c.Model.Provider) == "" || strings.TrimSpace(c.Model.Name) == "" {
		return fmt.Errorf("%w: model provider and name are required", ErrInvalidArgument)
	}
	if c.Model.MaxTokens < 0 {
		return fmt.Errorf("%w: max_tokens cannot be negative", ErrInvalidArgument)
	}
	if c.Model.Temperature != nil && (*c.Model.Temperature < 0 || *c.Model.Temperature > 2) {
		return fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalidArgument)
	}
	return nil
}

// Digest returns the immutable configuration fingerprint.
func (c RevisionConfig) Digest() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: encode revision config: %v", ErrInvalidArgument, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// AgentRevision is an immutable runtime configuration snapshot. Lifecycle
// metadata may advance, but Config and ConfigDigest never change after create.
type AgentRevision struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenant_id"`
	AgentAppID   string         `json:"agent_app_id"`
	RevisionNo   uint64         `json:"revision_no"`
	Config       RevisionConfig `json:"config"`
	ConfigDigest string         `json:"config_digest"`
	Status       RevisionStatus `json:"status"`
	CreatedBy    string         `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	PublishedAt  *time.Time     `json:"published_at,omitempty"`
}

func (r AgentRevision) validate(scope TenantContext) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if r.TenantID != scope.TenantID {
		return fmt.Errorf("%w: revision tenant %q does not match %q", ErrTenantScope, r.TenantID, scope.TenantID)
	}
	if err := ValidateResourceID("revision id", r.ID); err != nil {
		return err
	}
	if err := ValidateResourceID("app id", r.AgentAppID); err != nil {
		return err
	}
	if r.RevisionNo == 0 {
		return fmt.Errorf("%w: revision number must be positive", ErrInvalidArgument)
	}
	if strings.TrimSpace(r.CreatedBy) == "" {
		return fmt.Errorf("%w: created_by is required", ErrInvalidArgument)
	}
	if r.Status != RevisionStatusDraft {
		return fmt.Errorf("%w: a new revision must be draft", ErrInvalidArgument)
	}
	return r.Config.validate()
}
