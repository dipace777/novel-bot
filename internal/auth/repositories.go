package auth

import (
	"context"
	"time"
)

// Repository implementations must be safe for concurrent use.
type Repository interface {
	CreateClient(context.Context, Client) error
	CreateKey(context.Context, Key) error
	FindKey(context.Context, string) (Key, error)
	ListKeys(context.Context, string) ([]Key, error)
	RevokeKey(context.Context, string, string, time.Time) error
}

type AccountRepository interface {
	// CreateAccount persists the user and its client in a single transaction.
	CreateAccount(context.Context, User, Client) error
	FindUserByEmail(context.Context, string) (User, error)
	CreateSession(context.Context, Session) error
	FindSession(context.Context, string) (Session, User, error)
	RevokeSession(context.Context, string, string, time.Time) error
}
