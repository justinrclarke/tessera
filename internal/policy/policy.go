package policy

import "tessera/internal/api"

func Default() api.Policy {
	return api.Policy{
		Kind:          api.KindPolicy,
		Auto:          []string{"restart", "reschedule", "cordon", "uncordon", "rollback", "prune", "renew", "move", "hold"},
		Confirm:       []string{"delete", "wipe", "reimage"},
		MoveMinGain:   0.15,
		MoveCooldown:  "10m",
		SoloPromotion: "30s",
		MaxRestarts:   3,
		SuspectAfter:  "3s",
		DeadAfter:     "10s",
		LeaseTTL:      "5s",
	}
}

func Normalize(p api.Policy) api.Policy {
	d := Default()
	if len(p.Auto) == 0 {
		p.Auto = d.Auto
	}
	if len(p.Confirm) == 0 {
		p.Confirm = d.Confirm
	}
	if p.MoveMinGain == 0 {
		p.MoveMinGain = d.MoveMinGain
	}
	if p.MoveCooldown == "" {
		p.MoveCooldown = d.MoveCooldown
	}
	if p.SoloPromotion == "" {
		p.SoloPromotion = d.SoloPromotion
	}
	if p.MaxRestarts == 0 {
		p.MaxRestarts = d.MaxRestarts
	}
	if p.SuspectAfter == "" {
		p.SuspectAfter = d.SuspectAfter
	}
	if p.DeadAfter == "" {
		p.DeadAfter = d.DeadAfter
	}
	if p.LeaseTTL == "" {
		p.LeaseTTL = d.LeaseTTL
	}
	p.Kind = api.KindPolicy
	return p
}

func Decide(p api.Policy, action string) string {
	switch action {
	case "wipe", "reimage", "delete":
		return "confirm"
	}
	for _, a := range p.Auto {
		if a == action {
			return "auto"
		}
	}
	for _, a := range p.Confirm {
		if a == action {
			return "confirm"
		}
	}
	return "confirm"
}

func AllowsAuto(p api.Policy, action string) bool {
	return Decide(p, action) == "auto"
}
