package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Crockford-ish alphabet — no 0/O/1/I ambiguity when reading off a screen.
const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	pairCodeLen    = 8 // 32^8 ≈ 1.1e12 — resists casual LAN brute-force
	pairCodeTTL    = 10 * time.Minute
	pairExpiryTick = 2 * time.Second
)

func generatePairCode() (string, error) {
	max := big.NewInt(int64(len(pairAlphabet)))
	out := make([]byte, pairCodeLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = pairAlphabet[n.Int64()]
	}
	return string(out), nil
}

func normalizePairCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func phonePairURL(base, code string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://localhost:8443"
	}
	return fmt.Sprintf("%s/phone?t=%s", base, code)
}

func v4l2FixCommand(device string) string {
	nr := "10"
	if strings.HasPrefix(device, "/dev/video") {
		if n := strings.TrimPrefix(device, "/dev/video"); n != "" {
			nr = n
		}
	}
	return fmt.Sprintf(
		"sudo modprobe v4l2loopback devices=1 video_nr=%s card_label=Tether exclusive_caps=1",
		nr,
	)
}
