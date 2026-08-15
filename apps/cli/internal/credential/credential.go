package credential

import "time"

const skew = time.Minute

type Credential struct {
	Token   string    `yaml:"token,omitempty"`
	Refresh string    `yaml:"refresh_token,omitempty"`
	Expiry  time.Time `yaml:"expiry,omitempty"`
}

func (c Credential) IsZero() bool {
	return c.Token == "" && c.Refresh == ""
}

func (c Credential) IsExpired(now time.Time) bool {
	if c.Expiry.IsZero() {
		return false
	}
	return !now.Before(c.Expiry.Add(-skew))
}

func (c Credential) CanRefresh() bool {
	return c.Refresh != ""
}

type Store interface {
	Get(context string) (Credential, error)
	Set(context string, cred Credential) error
	Delete(context string) error

	Rename(from, to string) error

	Location() string
}
