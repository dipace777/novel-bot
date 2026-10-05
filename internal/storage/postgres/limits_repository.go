package postgres

import (
	"context"
	"fmt"
	"time"

	"novel-bot/internal/limits"
	"novel-bot/internal/storage/postgres/dbgen"
)

var _ limits.Provider = (*Repository)(nil)

func (r *Repository) Policy(ctx context.Context, clientID string) (limits.Policy, error) {
	row, err := r.queries.GetTenantPolicy(ctx, clientID)
	if err != nil {
		return limits.Policy{}, err
	}
	return limits.Policy{MaxConcurrentSessions: int(row.MaxConcurrentSessions), MaxSessionTTL: time.Duration(row.MaxSessionSeconds) * time.Second, SessionRequestsPerMinute: int(row.SessionRequestsPerMinute)}, nil
}

// SetPolicy is an operator-facing repository method; tenants cannot raise their
// own limits through the public API. Changes apply to subsequent creations.
func (r *Repository) SetPolicy(ctx context.Context, clientID string, policy limits.Policy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.MaxSessionTTL < time.Second || policy.MaxSessionTTL%time.Second != 0 {
		return fmt.Errorf("tenant lifetime must be a whole number of seconds")
	}
	count, err := r.queries.UpdateTenantPolicy(ctx, dbgen.UpdateTenantPolicyParams{ClientID: clientID, MaxConcurrentSessions: int32(policy.MaxConcurrentSessions), MaxSessionSeconds: int32(policy.MaxSessionTTL / time.Second), SessionRequestsPerMinute: int32(policy.SessionRequestsPerMinute)})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("tenant policy not found")
	}
	return nil
}
