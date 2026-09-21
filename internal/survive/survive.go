package survive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

type Lease struct {
	Epoch    uint64
	LeaderID string
	Expires  time.Time
}

type Claim struct {
	Epoch uint64
	ID    string
}

type Decision struct {
	Promote bool
	Epoch   uint64
	Reason  string
}

func Expired(l Lease, now time.Time) bool {
	if l.LeaderID == "" {
		return true
	}
	return !now.Before(l.Expires)
}

func Decide(lease Lease, lostAt, now time.Time, soloAfter time.Duration, others int) Decision {
	if !Expired(lease, now) {
		return Decision{Reason: "lease held"}
	}
	if lostAt.IsZero() {
		return Decision{Reason: "no failure observed"}
	}
	if others == 0 && now.Sub(lostAt) < soloAfter {
		return Decision{Promote: false, Reason: "solo promotion waiting"}
	}
	epoch := lease.Epoch + 1
	if epoch == 0 {
		epoch = 1
	}
	return Decision{Promote: true, Epoch: epoch, Reason: "leader lease expired"}
}

func ShouldLead(myID string, others []string) bool {
	ids := append([]string{myID}, others...)
	sort.Strings(ids)
	return ids[0] == myID
}

func Preferred(claims []Claim) Claim {
	if len(claims) == 0 {
		return Claim{}
	}
	best := claims[0]
	for _, c := range claims[1:] {
		if c.Epoch > best.Epoch || (c.Epoch == best.Epoch && c.ID < best.ID) {
			best = c
		}
	}
	return best
}

func Superseded(current, incoming uint64) bool {
	return incoming > current
}

func AcceptAssignment(maxEpoch, assignmentEpoch uint64) bool {
	if assignmentEpoch == 0 || maxEpoch == 0 {
		return true
	}
	return assignmentEpoch >= maxEpoch
}

func Sign(token string, payload []byte) string {
	m := hmac.New(sha256.New, []byte(token))
	_, _ = m.Write(payload)
	return hex.EncodeToString(m.Sum(nil))
}

func Verify(token string, payload []byte, sig string) bool {
	want := Sign(token, payload)
	return hmac.Equal([]byte(want), []byte(sig))
}
