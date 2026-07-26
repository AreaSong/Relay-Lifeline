package main

import "testing"

func TestValidateAdminKey(t *testing.T) {
	tests := []struct {
		name         string
		adminEnabled bool
		adminKey     string
		wantError    bool
	}{
		{name: "控制台关闭时允许空密钥", adminEnabled: false},
		{name: "控制台开启时拒绝空密钥", adminEnabled: true, wantError: true},
		{name: "控制台开启时拒绝过短密钥", adminEnabled: true, adminKey: "short-key", wantError: true},
		{name: "控制台开启时接受二十四字符密钥", adminEnabled: true, adminKey: "123456789012345678901234"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAdminKey(test.adminEnabled, test.adminKey)
			if (err != nil) != test.wantError {
				t.Fatalf("validateAdminKey() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}

func TestLocaleResolution(t *testing.T) {
	if locale, ok := environmentLocale("zh_CN.UTF-8"); !ok || locale != "zh-CN" {
		t.Fatalf("中文环境语言解析异常: %s %v", locale, ok)
	}
	if locale, ok := environmentLocale("C"); ok || locale != "en-US" {
		t.Fatalf("C 环境语言解析异常: %s %v", locale, ok)
	}
	if !hasLocaleArgument([]string{"--locale=en-US"}) || hasLocaleArgument([]string{"--config", "x"}) {
		t.Fatal("locale 参数识别异常")
	}
}
