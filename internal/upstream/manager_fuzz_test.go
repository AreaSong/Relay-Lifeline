package upstream

import (
	"context"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func FuzzManagerStateMachine(f *testing.F) {
	f.Add([]byte("select-release-observe-apply"))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 128 {
			operations = operations[:128]
		}
		m, err := New(poolForTests(), configForFuzz())
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range operations {
			switch operation % 5 {
			case 0:
				lease, selectErr := m.Select(context.Background(), SelectionContext{})
				if selectErr == nil {
					lease.Release()
				}
			case 1:
				m.Observe("a", Observation{Category: "transport", Latency: time.Millisecond})
			case 2:
				m.Observe("b", Observation{Success: true, Latency: time.Millisecond})
			case 3:
				lease, selectErr := m.Select(context.Background(), SelectionContext{PreviousTargetID: "a", PreviousDomain: "d1", WroteRequest: true, IdempotencyKey: "stable"})
				if selectErr == nil {
					lease.Complete(Observation{Success: true})
				}
			case 4:
				_ = m.Apply(poolForTests(), configForFuzz())
			}
			for _, target := range m.Snapshot().Targets {
				if target.Active < 0 || target.HalfOpenLeases < 0 {
					t.Fatalf("negative target counters: %+v", target)
				}
			}
		}
	})
}

func configForFuzz() config.UpstreamConfig {
	return config.Default().Upstream
}
