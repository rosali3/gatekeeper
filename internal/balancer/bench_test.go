package balancer

import "testing"

func BenchmarkRoundRobin_Pick(b *testing.B) {
	bal, err := New("round_robin", []TargetSpec{
		{URL: "http://a", Weight: 1}, {URL: "http://b", Weight: 1}, {URL: "http://c", Weight: 1},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, release, _ := bal.Pick()
		release()
	}
}

func BenchmarkWeightedRoundRobin_Pick(b *testing.B) {
	bal, err := New("weighted_round_robin", []TargetSpec{
		{URL: "http://a", Weight: 5}, {URL: "http://b", Weight: 1}, {URL: "http://c", Weight: 1},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, release, _ := bal.Pick()
		release()
	}
}

func BenchmarkLeastConn_Pick(b *testing.B) {
	bal, err := New("least_conn", []TargetSpec{
		{URL: "http://a", Weight: 1}, {URL: "http://b", Weight: 1}, {URL: "http://c", Weight: 1},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, release, _ := bal.Pick()
		release()
	}
}

func BenchmarkRoundRobin_Pick_Parallel(b *testing.B) {
	bal, err := New("round_robin", []TargetSpec{
		{URL: "http://a", Weight: 1}, {URL: "http://b", Weight: 1}, {URL: "http://c", Weight: 1},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, release, _ := bal.Pick()
			release()
		}
	})
}
