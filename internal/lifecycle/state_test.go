package lifecycle

import "testing"

func TestRequestLifecycleAcceptsRecoveryPath(t *testing.T) {
	path := []State{StateReceived, StateQueued, StateForwarding, StateUncertain, StateWaiting, StateForwarding, StateBuffering, StateDelivering, StateCompleted, StateSuccessful}
	for index := 1; index < len(path); index++ {
		if err := ValidateTransition(path[index-1], path[index]); err != nil {
			t.Fatalf("合法恢复路径被拒绝: %v", err)
		}
	}
}

func TestRequestLifecycleRejectsTerminalReplay(t *testing.T) {
	if err := ValidateTransition(StateSuccessful, StateForwarding); err == nil {
		t.Fatal("终态不应重新进入上游请求")
	}
	if !IsTerminal(StateOrphaned) || IsTerminal(StateWaiting) {
		t.Fatal("终态判断异常")
	}
}
