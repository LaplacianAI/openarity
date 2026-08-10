package auth

import (
	"context"
	"crypto/subtle"
	"errors"
)

type devVerifier struct {
	token string
}

func NewDevVerifier(token string) (Verifier, error) {
	if token == "" {
		return nil, errors.New("auth: dev token must not be empty")
	}
	return devVerifier{token: token}, nil
}

func (d devVerifier) Verify(_ context.Context, token string) (*Principal, error) {
	if subtle.ConstantTimeCompare([]byte(token), []byte(d.token)) != 1 {
		return nil, ErrUnauthenticated
	}
	return &Principal{Kind: KindDev, Subject: "dev"}, nil
}
