package auth

import "errors"

type Kind string

const (
	KindUser    Kind = "user"
	KindService Kind = "service"
	KindDev     Kind = "dev"
)

type Principal struct {
	Kind    Kind
	Issuer  string
	Subject string
	Email   string
}

var ErrUnauthenticated = errors.New("unauthenticated")
