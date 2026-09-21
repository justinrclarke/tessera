package survive

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Unix(1_000, 0)
	lease := Lease{Epoch: 3, LeaderID: "a", Expires: now.Add(time.Second)}
	if d := Decide(lease, now.Add(-time.Minute), now, 30*time.Second, 0); d.Promote {
		t.Fatal("held lease must not promote")
	}
	lease.Expires = now.Add(-time.Second)
	if d := Decide(lease, now.Add(-10*time.Second), now, 30*time.Second, 0); d.Promote || d.Reason != "solo promotion waiting" {
		t.Fatalf("solo wait: %+v", d)
	}
	if d := Decide(lease, now.Add(-31*time.Second), now, 30*time.Second, 0); !d.Promote || d.Epoch != 4 {
		t.Fatalf("solo after timeout: %+v", d)
	}
	if d := Decide(lease, now.Add(-time.Second), now, 30*time.Second, 2); !d.Promote {
		t.Fatalf("peers visible should promote: %+v", d)
	}
}

func TestPreferredAndFence(t *testing.T) {
	got := Preferred([]Claim{{Epoch: 2, ID: "b"}, {Epoch: 3, ID: "c"}, {Epoch: 3, ID: "a"}})
	if got.ID != "a" || got.Epoch != 3 {
		t.Fatalf("%+v", got)
	}
	if !Superseded(2, 3) || Superseded(3, 3) {
		t.Fatal("epoch compare")
	}
	if AcceptAssignment(3, 2) {
		t.Fatal("old epoch assignment must be rejected")
	}
	if !AcceptAssignment(3, 3) {
		t.Fatal("current epoch must be accepted")
	}
	if ShouldLead("b", []string{"a", "c"}) {
		t.Fatal("lowest id leads")
	}
	if !ShouldLead("a", []string{"b"}) {
		t.Fatal("expected lead")
	}
}

func TestSign(t *testing.T) {
	sig := Sign("tok", []byte("snap"))
	if !Verify("tok", []byte("snap"), sig) || Verify("other", []byte("snap"), sig) {
		t.Fatal("hmac")
	}
}
