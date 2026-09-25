package balancer

import "testing"

func TestNew_AllAlgorithmsImplemented(t *testing.T) {
	tests := []struct {
		algo string
		want any
	}{
		{"round_robin", &RoundRobin{}},
		{"weighted_round_robin", &WeightedRoundRobin{}},
		{"least_conn", &LeastConn{}},
	}
	for _, tt := range tests {
		b, err := New(tt.algo, []TargetSpec{{URL: "http://a", Weight: 1}})
		if err != nil {
			t.Errorf("New(%q): unexpected error: %v", tt.algo, err)
			continue
		}
		switch tt.want.(type) {
		case *RoundRobin:
			if _, ok := b.(*RoundRobin); !ok {
				t.Errorf("New(%q): got %T, want *RoundRobin", tt.algo, b)
			}
		case *WeightedRoundRobin:
			if _, ok := b.(*WeightedRoundRobin); !ok {
				t.Errorf("New(%q): got %T, want *WeightedRoundRobin", tt.algo, b)
			}
		case *LeastConn:
			if _, ok := b.(*LeastConn); !ok {
				t.Errorf("New(%q): got %T, want *LeastConn", tt.algo, b)
			}
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

func TestNew_TargetsStartHealthy(t *testing.T) {
	b, err := New("round_robin", []TargetSpec{{URL: "http://a", Weight: 1}})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if !b.Targets()[0].Healthy() {
		t.Error("a freshly built target should start healthy")
	}
}
