package auth

import "context"

type Verifier interface {
	Verify(ctx context.Context, token string) (*Principal, error)
}

type Chain []Verifier

func (c Chain) Verify(ctx context.Context, token string) (*Principal, error) {
	for _, v := range c {
		if p, err := v.Verify(ctx, token); err == nil && p != nil {
			return p, nil
		}
	}
	return nil, ErrUnauthenticated
}
