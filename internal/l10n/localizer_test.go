package l10n

import (
	"errors"
	"testing"
)

func TestCatalogTranslatesAndFallsBack(t *testing.T) {
	message := M("risk.many_attempts", map[string]any{"Attempts": 12})
	if got := Default.Text(LocaleChinese, LocaleEnglish, message); got != "请求已尝试 12 次，存在重复调用或计费风险" {
		t.Fatalf("中文翻译异常: %s", got)
	}
	if got := Default.Text(LocaleEnglish, LocaleChinese, message); got != "The request has been attempted 12 times and may cause duplicate calls or charges" {
		t.Fatalf("英文翻译异常: %s", got)
	}
	if got := Default.Text("fr-FR", LocaleChinese, M("api.admin.invalid_key")); got != "管理密钥无效" {
		t.Fatalf("回退语言异常: %s", got)
	}
}

func TestAcceptLanguageAndLocalizedErrors(t *testing.T) {
	if got := FromAcceptLanguage("en-GB,en;q=0.9", LocaleChinese); got != LocaleEnglish {
		t.Fatalf("语言匹配异常: %s", got)
	}
	if got := FromAcceptLanguage("zh-Hans,zh;q=0.9", LocaleEnglish); got != LocaleChinese {
		t.Fatalf("语言匹配异常: %s", got)
	}
	err := errors.Join(E("config.server.listen_required", nil), E("config.retry.max_attempts", nil))
	message := Default.Error(LocaleEnglish, LocaleChinese, err)
	if message != "server.listen cannot be empty; retry.max-attempts cannot be less than 0" {
		t.Fatalf("组合错误翻译异常: %s", message)
	}
}
