package whoami

type teamView struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
	Role string `json:"role" yaml:"role"`
}

type whoamiView struct {
	Kind    string     `json:"kind" yaml:"kind"`
	Subject string     `json:"subject" yaml:"subject"`
	ID      string     `json:"id" yaml:"id"`
	Issuer  string     `json:"issuer,omitempty" yaml:"issuer,omitempty"`
	Email   string     `json:"email,omitempty" yaml:"email,omitempty"`
	Teams   []teamView `json:"teams" yaml:"teams"`
}
