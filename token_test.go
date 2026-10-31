package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestTokenRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, info, err := NewToken(testSecret, 24*time.Hour, "Zażółć Gęślą", now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseToken(testSecret, tok, now.Add(23*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != info || got.Name != "Zażółć Gęślą" || !got.Expires.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("mismatch: %+v vs %+v", got, info)
	}
	if strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token not URL-safe: %s", tok)
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, _, _ := NewToken(testSecret, time.Hour, "Kasia", now)
	got, err := ParseToken(testSecret, tok, now.Add(time.Hour))
	if !errors.Is(err, ErrExpired) || got.Name != "Kasia" {
		t.Fatalf("want expired, got %v %+v", err, got)
	}
}

func TestTokenTampered(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, _, _ := NewToken(testSecret, time.Hour, "", now)
	if _, err := ParseToken([]byte("another-secret-another-secret-xx"), tok, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong secret: %v", err)
	}
	b := []byte(tok)
	b[3] ^= 1
	if _, err := ParseToken(testSecret, string(b), now); err == nil {
		t.Fatal("tampered token accepted")
	}
	for _, s := range []string{"", "abc", "!!!!", strings.Repeat("A", 500)} {
		if _, err := ParseToken(testSecret, s, now); err == nil {
			t.Fatalf("garbage %q accepted", s)
		}
	}
}

func TestTokenNameLimit(t *testing.T) {
	if _, _, err := NewToken(testSecret, time.Hour, strings.Repeat("x", maxNameLen+1), time.Now()); err == nil {
		t.Fatal("too long name accepted")
	}
	if _, _, err := NewToken(testSecret, 0, "", time.Now()); err == nil {
		t.Fatal("zero ttl accepted")
	}
}

func TestParseTTL(t *testing.T) {
	cases := map[string]time.Duration{
		"24h": 24 * time.Hour, "2d": 48 * time.Hour, "1d12h": 36 * time.Hour, "90m": 90 * time.Minute,
	}
	for in, want := range cases {
		got, err := ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("%s: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"x", "d", "1x"} {
		if _, err := ParseTTL(in); err == nil {
			t.Errorf("%s: expected error", in)
		}
	}
}
