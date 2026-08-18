package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SigningKeyIDEnvironment  = "RELAY_LIFELINE_WEBHOOK_SIGNING_KEY_ID"
	SigningSecretEnvironment = "RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET"
	SignatureHeader          = "X-Relay-Lifeline-Signature"
	SignatureTimestampHeader = "X-Relay-Lifeline-Signature-Timestamp"
	SignatureKeyIDHeader     = "X-Relay-Lifeline-Signature-Key-ID"
)

var (
	ErrSigningKeyIDRequired  = errors.New("webhook signing key id is required")
	ErrSigningSecretRequired = errors.New("webhook signing secret is required")
	ErrSigningSecretShort    = errors.New("webhook signing secret must contain at least 32 bytes")
)

type SigningConfig struct {
	KeyID  string
	Secret string
}

func ValidateSigningConfig(config SigningConfig, webhookConfigured bool) error {
	if !webhookConfigured && config.KeyID == "" && config.Secret == "" {
		return nil
	}
	if config.KeyID == "" {
		return ErrSigningKeyIDRequired
	}
	if config.Secret == "" {
		return ErrSigningSecretRequired
	}
	if len([]byte(config.Secret)) < 32 {
		return ErrSigningSecretShort
	}
	if strings.TrimSpace(config.KeyID) != config.KeyID || strings.ContainsAny(config.KeyID, "\r\n") {
		return fmt.Errorf("invalid webhook signing key id")
	}
	return nil
}

func (config SigningConfig) Configured() bool {
	return config.KeyID != "" && config.Secret != ""
}

func (config SigningConfig) Sign(payload []byte, now time.Time) (timestamp, signature string) {
	timestamp = fmt.Sprintf("%d", now.Unix())
	message := []byte(timestamp + "." + string(payload))
	hasher := hmac.New(sha256.New, []byte(config.Secret))
	_, _ = hasher.Write(message)
	return timestamp, "v1=" + hex.EncodeToString(hasher.Sum(nil))
}
