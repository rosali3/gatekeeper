package balancer

import "testing"

func TestNew_RoundRobin(t *testing.T) {
	b, err := New("round_robin", []TargetSpec{{URL: "http://a", Weight: 1}})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if _, ok := b.(*RoundRobin); !ok {
		t.Fatalf("New: got %T, want *RoundRobin", b)
	}
}

func TestNew_NotYetImplemented(t *testing.T) {
	for _, algo := range []string{"weighted_round_robin", "least_conn"} {
		if _, err := New(algo, []TargetSpec{{URL: "http://a", Weight: 1}}); err == nil {
			t.Errorf("New(%q): expected a not-implemented error, got nil", algo)
		}
	}
}

func TestNew_UnknownAlgorithm(t *testing.T) {
	if _, err := New("bogus", []TargetSpec{{URL: "http://a", Weight: 1}}); err == nil {
		t.Fatal("New(\"bogus\"): expected error, got nil")
	}
}

func TestNew_InvalidTargetURL(t *testing.T) {
	if _, err := New("round_robin", []TargetSpec{{URL: "http://[::1", Weight: 1}}); err == nil {
		t.Fatal("New: expected error for invalid target URL, got nil")
	}
}
