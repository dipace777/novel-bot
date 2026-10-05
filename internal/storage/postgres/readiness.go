package postgres

import (
	"context"
)

// Ready checks connectivity, schema availability, and required read permissions.
func (r *Repository) Ready(ctx context.Context) error {
	return r.queries.CheckAuthSchema(ctx)
}
