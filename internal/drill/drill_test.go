package drill

import "testing"

func TestRun(t *testing.T) {
	reports, err := Run()
	for _, r := range reports {
		if !r.Pass {
			t.Errorf("%s: %s", r.Name, r.Detail)
		}
	}
	if err != nil && !t.Failed() {
		t.Fatal(err)
	}
}
