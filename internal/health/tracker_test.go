package health

import (
	"testing"

	"gatekeeper/internal/balancer"
)

func newTestTarget(t *testing.T) *balancer.Target {
	t.Helper()
	b, err := balancer.New("round_robin", []balancer.TargetSpec{{URL: "http://backend", Weight: 1}})
	if err != nil {
		t.Fatalf("balancer.New: %v", err)
	}
	return b.Targets()[0]
}

func TestTracker_StartsHealthyAndStaysHealthyOnSuccess(t *testing.T) {
	target := newTestTarget(t)
	tracker := NewTracker(target, 2, 3)

	if transitioned := tracker.ReportSuccess(); transitioned {
		t.Error("ReportSuccess on an already-healthy target should not report a transition")
	}
	if !target.Healthy() {
		t.Error("target should still be healthy")
	}
}

func TestTracker_UnhealthyAfterThresholdFailures(t *testing.T) {
	target := newTestTarget(t)
	tracker := NewTracker(target, 2, 3)

	if tracker.ReportFailure() {
		t.Fatal("failure #1 should not yet cross the unhealthy threshold")
	}
	if tracker.ReportFailure() {
		t.Fatal("failure #2 should not yet cross the unhealthy threshold")
	}
	if !target.Healthy() {
		t.Fatal("target should still be healthy before the threshold is reached")
	}
	if !tracker.ReportFailure() {
		t.Fatal("failure #3 should cross the unhealthy threshold and report a transition")
	}
	if target.Healthy() {
		t.Fatal("target should now be unhealthy")
	}
}

func TestTracker_HealthyAgainAfterThresholdSuccesses(t *testing.T) {
	target := newTestTarget(t)
	tracker := NewTracker(target, 2, 1)

	tracker.ReportFailure() // unhealthyThreshold=1, so this already flips it
	if target.Healthy() {
		t.Fatal("target should be unhealthy after one failure (threshold=1)")
	}

	if tracker.ReportSuccess() {
		t.Fatal("success #1 should not yet cross the healthy threshold (2)")
	}
	if target.Healthy() {
		t.Fatal("target should still be unhealthy before the threshold is reached")
	}
	if !tracker.ReportSuccess() {
		t.Fatal("success #2 should cross the healthy threshold and report a transition")
	}
	if !target.Healthy() {
		t.Fatal("target should now be healthy again")
	}
}

func TestTracker_FailureResetsSuccessStreak(t *testing.T) {
	target := newTestTarget(t)
	tracker := NewTracker(target, 2, 2)

	tracker.ReportFailure()
	tracker.ReportFailure() // crosses unhealthyThreshold=2
	if target.Healthy() {
		t.Fatal("target should be unhealthy")
	}

	tracker.ReportSuccess() // 1 consecutive success - not enough yet
	tracker.ReportFailure() // resets the success streak back to 0
	if transitioned := tracker.ReportSuccess(); transitioned {
		t.Fatal("a single success after the streak was reset should not cross healthyThreshold=2")
	}
	if target.Healthy() {
		t.Fatal("target should still be unhealthy: only one success since the last failure")
	}
}
