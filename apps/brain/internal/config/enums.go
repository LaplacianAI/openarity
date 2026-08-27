package config

import (
	"fmt"
)

type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentProduction  Environment = "production"
	EnvironmentStaging     Environment = "staging"
	EnvironmentTest        Environment = "test"
)

func (e *Environment) UnmarshalText(text []byte) error {
	switch v := Environment(text); v {
	case EnvironmentDevelopment, EnvironmentProduction, EnvironmentStaging, EnvironmentTest:
		*e = v
		return nil
	default:
		return fmt.Errorf("invalid environment: %s", v)
	}
}

type SecretsBackend string

const (
	SecretsBackendStatic  SecretsBackend = "static"
	SecretsBackendOpenBao SecretsBackend = "openbao"
)

func (b *SecretsBackend) UnmarshalText(text []byte) error {
	switch v := SecretsBackend(text); v {
	case SecretsBackendStatic, SecretsBackendOpenBao:
		*b = v
		return nil
	default:
		return fmt.Errorf("invalid secrets backend: %s (want static or openbao)", v)
	}
}

type ObjectsBackend string

const (
	ObjectsBackendMemory     ObjectsBackend = "memory"
	ObjectsBackendFilesystem ObjectsBackend = "filesystem"
	ObjectsBackendS3         ObjectsBackend = "s3"
)

func (b *ObjectsBackend) UnmarshalText(text []byte) error {
	switch v := ObjectsBackend(text); v {
	case ObjectsBackendMemory, ObjectsBackendFilesystem, ObjectsBackendS3:
		*b = v
		return nil
	default:
		return fmt.Errorf(
			"invalid objects backend: %s (want memory, filesystem or s3)", v)
	}
}
