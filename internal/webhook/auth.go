package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	gitlabTokenHeader      = "X-Gitlab-Token"
	webhookIDHeader        = "Webhook-Id"
	webhookTimestampHeader = "Webhook-Timestamp"
	webhookSignatureHeader = "Webhook-Signature"
	signingTokenPrefix     = "whsec_"
	signatureTTL           = 5 * time.Minute
)

func requestAuthorized(r *http.Request, body []byte, settings Settings) bool {
	signature := r.Header.Get(webhookSignatureHeader)
	if settings.SigningToken != "" && signature != "" {
		return signatureMatches(settings.SigningToken, r.Header.Get(webhookIDHeader), r.Header.Get(webhookTimestampHeader), body, signature, time.Now())
	}
	if settings.Secret != "" {
		return secretMatches(r.Header.Get(gitlabTokenHeader), settings.Secret)
	}
	return false
}

func secretMatches(provided, secret string) bool {
	if secret == "" {
		return false
	}
	got := sha256.Sum256([]byte(provided))
	want := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func signatureMatches(signingToken, messageID, timestamp string, body []byte, received string, now time.Time) bool {
	if signingToken == "" || messageID == "" || timestamp == "" || received == "" {
		return false
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	sent := time.Unix(unix, 0)
	delta := now.Sub(sent)
	if delta < 0 {
		delta = -delta
	}
	if delta > signatureTTL {
		return false
	}
	key, err := decodeSigningKey(signingToken)
	if err != nil || len(key) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(messageID + "." + timestamp + "."))
	_, _ = mac.Write(body)
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, sig := range strings.Fields(received) {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return true
		}
	}
	return false
}

func decodeSigningKey(token string) ([]byte, error) {
	raw := strings.TrimSpace(token)
	raw = strings.TrimPrefix(raw, signingTokenPrefix)
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(raw)
}
