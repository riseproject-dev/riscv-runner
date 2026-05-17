package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// verifySignature checks an X-Hub-Signature-256 header against the body.
// The failure message is logged but not echoed to the client, since it can
// leak the expected signature.
func verifySignature(body []byte, signature, secret string) (bool, string) {
	if signature == "" {
		return false, "X-Hub-Signature-256 header is missing!"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return false, "Request signatures didn't match!"
	}
	return true, ""
}
