package l10n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

const (
	LocaleChinese = "zh-CN"
	LocaleEnglish = "en-US"
)

//go:embed locales/*.json
var localeFiles embed.FS

type Message struct {
	ID   string         `json:"code,omitempty"`
	Data map[string]any `json:"details,omitempty"`
}

type Error struct {
	Message
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.ID
}

func (e *Error) Unwrap() error { return e.Cause }

type Catalog struct {
	bundle *i18n.Bundle
}

func NewCatalog() *Catalog {
	bundle := i18n.NewBundle(language.AmericanEnglish)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	entries, err := fs.Glob(localeFiles, "locales/*.json")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if _, err := bundle.LoadMessageFileFS(localeFiles, entry); err != nil {
			panic(fmt.Sprintf("load locale %s: %v", entry, err))
		}
	}
	return &Catalog{bundle: bundle}
}

var Default = NewCatalog()

func M(id string, data ...map[string]any) Message {
	message := Message{ID: id}
	if len(data) > 0 {
		message.Data = data[0]
	}
	return message
}

func E(id string, cause error, data ...map[string]any) error {
	message := M(id, data...)
	return &Error{Message: message, Cause: cause}
}

func Normalize(raw string) string {
	tag, _, confidence := language.NewMatcher([]language.Tag{language.SimplifiedChinese, language.AmericanEnglish}).Match(language.Make(strings.TrimSpace(raw)))
	if confidence == language.No {
		return ""
	}
	base, _ := tag.Base()
	if base.String() == "zh" {
		return LocaleChinese
	}
	return LocaleEnglish
}

func IsSupported(raw string) bool {
	value := strings.TrimSpace(raw)
	return value == LocaleChinese || value == LocaleEnglish
}

func FromAcceptLanguage(header, fallback string) string {
	if strings.TrimSpace(header) == "" {
		return supportedOr(fallback, LocaleEnglish)
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(tags) == 0 {
		return supportedOr(fallback, LocaleEnglish)
	}
	matcher := language.NewMatcher([]language.Tag{language.SimplifiedChinese, language.AmericanEnglish})
	_, index, confidence := matcher.Match(tags...)
	if confidence == language.No {
		return supportedOr(fallback, LocaleEnglish)
	}
	if index == 0 {
		return LocaleChinese
	}
	return LocaleEnglish
}

func (c *Catalog) Text(locale, fallback string, message Message) string {
	if message.ID == "" {
		return ""
	}
	localizer := i18n.NewLocalizer(c.bundle, supportedOr(locale, fallback), supportedOr(fallback, LocaleEnglish))
	value, err := localizer.Localize(&i18n.LocalizeConfig{MessageID: message.ID, TemplateData: message.Data})
	if err != nil {
		return message.ID
	}
	return value
}

func (c *Catalog) Error(locale, fallback string, err error) string {
	if err == nil {
		return ""
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		parts := make([]string, 0, len(joined.Unwrap()))
		for _, part := range joined.Unwrap() {
			parts = append(parts, c.Error(locale, fallback, part))
		}
		return strings.Join(parts, "; ")
	}
	var localized *Error
	if errors.As(err, &localized) {
		return c.Text(locale, fallback, localized.Message)
	}
	return err.Error()
}

func supportedOr(locale, fallback string) string {
	if IsSupported(locale) {
		return locale
	}
	if IsSupported(fallback) {
		return fallback
	}
	return LocaleEnglish
}
