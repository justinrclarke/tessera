package api

type CloudIdentity struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Region   string `json:"region"`
	Owned    bool   `json:"owned"`
	Owner    string `json:"owner"`
}
