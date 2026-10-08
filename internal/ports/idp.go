package ports

import (
	"context"
	"errors"
)

var ErrInvalidToken = errors.New("ports: invalid or expired token")

type Claims struct {
	Subject    string
	ProviderID string
	Roles      []string
}

func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type TokenValidator interface {
	ValidateToken(ctx context.Context, rawToken string) (Claims, error)
}
