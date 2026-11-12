package api

type BuildRecord struct {
	Image          string   `json:"image" yaml:"image"`
	Base           string   `json:"base" yaml:"base"`
	PackageManager string   `json:"package_manager,omitempty" yaml:"package_manager,omitempty"`
	Packages       []string `json:"packages,omitempty" yaml:"packages,omitempty"`
	Command        []string `json:"command" yaml:"command"`
	ContextDigest  string   `json:"context_digest" yaml:"context_digest"`
}
