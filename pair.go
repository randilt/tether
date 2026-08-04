package main

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Crockford-ish alphabet — no 0/O/1/I ambiguity when reading off a screen.
const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func generatePairCode() (string, error) {
	const n = 6
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range raw {
		out[i] = pairAlphabet[int(raw[i])%len(pairAlphabet)]
	}
	return string(out), nil
}

func normalizePairCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func phonePairURL(host, code string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost:8443"
	}
	return fmt.Sprintf("https://%s/phone?t=%s", host, code)
}
