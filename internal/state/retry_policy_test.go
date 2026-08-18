package state

import (
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/lifecycle"
)

func TestRetryPolicyValidationCoversEverySchedule(t *testing.T) {
	valid := []RetryPolicySpec{
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleInherit}, HonorRetryAfter: true},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleImmediate}, MaxAdditionalAttempts: 3},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleFixed, Interval: 5 * time.Second}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleRandom, Minimum: 5 * time.Second, Maximum: 30 * time.Second}},
		{Duration: time.Hour, Schedule: RetrySchedule{Mode: RetryScheduleExponential, Base: 5 * time.Second, Maximum: 5 * time.Minute}},
	}
	for _, spec := range valid {
		if err := spec.Validate(); err != nil {
			t.Fatalf("有效策略被拒绝: %+v err=%v", spec, err)
		}
	}
	invalid := []RetryPolicySpec{
		{Duration: time.Second, Schedule: RetrySchedule{Mode: RetryScheduleInherit}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleImmediate}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleImmediate}, MaxAdditionalAttempts: MaximumImmediateAttempts + 1},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleFixed, Interval: time.Minute}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleRandom, Minimum: 30 * time.Second, Maximum: 5 * time.Second}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleRandom, Minimum: 5 * time.Second, Maximum: time.Minute}},
		{Duration: time.Minute, Schedule: RetrySchedule{Mode: RetryScheduleExponential, Base: time.Second, Maximum: 30 * time.Second}},
	}
	for _, spec := range invalid {
		if err := spec.Validate(); err == nil {
			t.Fatalf("无效策略未被拒绝: %+v", spec)
		}
	}
}

func TestRegistryPolicyActivationRescheduleAndReset(t *testing.T) {
	registry := NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	changes := registry.PolicyChanges(id)
	registry.UpdateMessage(id, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateWaiting, 1, l10n.Message{}, time.Now().Add(time.Minute))
	spec := RetryPolicySpec{
		Duration:              time.Minute,
		Schedule:              RetrySchedule{Mode: RetryScheduleRandom, Minimum: 5 * time.Second, Maximum: 15 * time.Second},
		MaxAdditionalAttempts: 5, HonorRetryAfter: true,
	}
	if result := registry.ApplyRetryPolicy(id, spec, true); result.Outcome != RequestActionAccepted {
		t.Fatalf("等待请求策略设置失败: %+v", result)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("等待请求没有收到重排信号")
	}
	policy, ok := registry.RetryPolicy(id)
	if !ok || !policy.Active() || policy.BaselineAttempt != 1 || policy.Deadline.IsZero() {
		t.Fatalf("等待请求策略未立即激活: %+v", policy)
	}
	if result := registry.ClearRetryPolicy(id); result.Outcome != RequestActionAccepted {
		t.Fatalf("重置策略失败: %+v", result)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("重置策略没有触发等待重排")
	}
	if _, ok := registry.RetryPolicy(id); ok {
		t.Fatal("重置后仍存在单请求策略")
	}
}

func TestRegistryPendingPolicyActivatesAfterFailure(t *testing.T) {
	registry := NewRegistry()
	id, _ := registry.Add("POST", "/v1/responses", func() {})
	registry.UpdateMessage(id, lifecycle.StateForwarding, 2, l10n.Message{}, time.Time{})
	spec := RetryPolicySpec{Duration: time.Hour, Schedule: RetrySchedule{Mode: RetryScheduleFixed, Interval: time.Minute}}
	if result := registry.ApplyRetryPolicy(id, spec, true); result.Outcome != RequestActionAccepted {
		t.Fatalf("请求中策略设置失败: %+v", result)
	}
	pending, _ := registry.RetryPolicy(id)
	if pending.Active() || !pending.Deadline.IsZero() {
		t.Fatalf("请求中策略不应提前消耗恢复窗口: %+v", pending)
	}
	before := time.Now()
	active, ok := registry.ActivateRetryPolicy(id, 2)
	if !ok || !active.Active() || active.BaselineAttempt != 2 || active.Deadline.Before(before.Add(59*time.Minute)) {
		t.Fatalf("策略激活结果异常: %+v", active)
	}
}

func TestCheckedRetryEnforcesStateAndConfirmation(t *testing.T) {
	registry := NewRegistry()
	id, retry := registry.Add("POST", "/v1/responses", func() {})
	if result := registry.RetryNowChecked(id, false); result.Reason != RequestReasonStateNotRetryable {
		t.Fatalf("排队请求不应立即重试: %+v", result)
	}
	registry.UpdateMessage(id, lifecycle.StateForwarding, 1, l10n.Message{}, time.Time{})
	registry.UpdateMessage(id, lifecycle.StateUncertain, 1, l10n.Message{}, time.Time{})
	if result := registry.RetryNowChecked(id, false); result.Reason != RequestReasonConfirmationRequired {
		t.Fatalf("不确定交付应要求确认: %+v", result)
	}
	if result := registry.RetryNowChecked(id, true); result.Outcome != RequestActionAccepted {
		t.Fatalf("确认后应接受重试: %+v", result)
	}
	select {
	case <-retry:
	case <-time.After(time.Second):
		t.Fatal("确认后的重试信号未送达")
	}
}
