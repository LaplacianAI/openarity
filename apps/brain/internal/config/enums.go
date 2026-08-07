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
