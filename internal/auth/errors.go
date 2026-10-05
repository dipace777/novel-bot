package auth

import (
	"errors"
)

var (
	ErrUnauthorized = errors.New("invalid authentication credential")
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")
)

var (
	ErrConflict    = errors.New("email already registered")
	ErrCredentials = errors.New("invalid email or password")
)
