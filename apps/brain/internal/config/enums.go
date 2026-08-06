package config

import "fmt"

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

type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

func (l *LogLevel) UnmarshalText(text []byte) error {
	switch v := LogLevel(text); v {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		*l = v
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", v)
	}
}
