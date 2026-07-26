package risk

import (
	"fmt"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func TestRiskAlertsAreNonRepeatingAndResolvable(t *testing.T) {
	manager := New()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	cfg := config.Default().Risk
	cfg.WarningAttempts = 2
	cfg.AuthErrorAttempts = 2
	cfg.WarningAfter.Duration = time.Minute
	started := now.Add(-2 * time.Minute)

	first := manager.EvaluateAttempt("request", 2, started, 401, cfg)
	second := manager.EvaluateAttempt("request", 3, started, 401, cfg)
	if len(first) != 2 || len(second) != 1 || second[0].Type != "auth_errors" {
		t.Fatalf("风险提醒异常: first=%+v second=%+v", first, second)
	}
	if repeated := manager.EvaluateAttempt("request", 4, started, 401, cfg); len(repeated) != 0 {
		t.Fatalf("风险提醒不应重复: %+v", repeated)
	}
	manager.ResolveRequest("request")
	for _, alert := range manager.Recent(10) {
		if alert.ResolvedAt == nil {
			t.Fatalf("请求完成后提醒未解决: %+v", alert)
		}
	}
}

func TestAlertEvictionClearsDeduplicationIndex(t *testing.T) {
	manager := New()
	manager.RecordGlobal("oldest", "warning", "oldest")
	for index := 0; index < maxAlerts; index++ {
		manager.RecordGlobal(fmt.Sprintf("alert-%d", index), "warning", "test")
	}
	if alerts := manager.RecordGlobal("oldest", "warning", "oldest again"); len(alerts) != 1 {
		t.Fatalf("被淘汰告警的去重索引未清理: %+v", alerts)
	}
}

func TestQueuePressureAlert(t *testing.T) {
	manager := New()
	cfg := config.Default().Risk
	if alerts := manager.EvaluateQueue(8, 80, 8, 100, cfg); len(alerts) != 1 {
		t.Fatalf("应产生队列压力提醒: %+v", alerts)
	}
	if alerts := manager.EvaluateQueue(8, 80, 8, 100, cfg); len(alerts) != 0 {
		t.Fatalf("队列提醒不应重复: %+v", alerts)
	}
	manager.EvaluateQueue(1, 0, 8, 100, cfg)
	if alerts := manager.EvaluateQueue(8, 80, 8, 100, cfg); len(alerts) != 1 {
		t.Fatalf("恢复后再次过载应重新提醒: %+v", alerts)
	}
}

func TestAuthWarningsCoverUnauthorizedAndForbidden(t *testing.T) {
	for _, statusCode := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", statusCode), func(t *testing.T) {
			manager := New()
			cfg := config.Default().Risk
			cfg.AuthErrorAttempts = 2
			started := time.Now()
			if alerts := manager.EvaluateAttempt("request", 1, started, statusCode, cfg); len(alerts) != 0 {
				t.Fatalf("第一次鉴权错误不应提醒: %+v", alerts)
			}
			alerts := manager.EvaluateAttempt("request", 2, started, statusCode, cfg)
			if len(alerts) != 1 || alerts[0].Type != "auth_errors" {
				t.Fatalf("HTTP %d 未触发鉴权提醒: %+v", statusCode, alerts)
			}
		})
	}
}

func TestNonAuthFailureBreaksConsecutiveAuthErrors(t *testing.T) {
	manager := New()
	cfg := config.Default().Risk
	cfg.AuthErrorAttempts = 2
	started := time.Now()
	manager.EvaluateAttempt("request", 1, started, 401, cfg)
	manager.EvaluateAttempt("request", 2, started, 0, cfg)
	if alerts := manager.EvaluateAttempt("request", 3, started, 401, cfg); len(alerts) != 0 {
		t.Fatalf("网络错误后鉴权错误计数应重新开始: %+v", alerts)
	}
}
