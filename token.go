package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Token layout (base64url, no padding):
//
//	version(1) | expiry unix seconds(8) | id(6) | name(0..maxNameLen) | hmac-sha256[:16]
const (
	tokenVersion = 1
	idLen        = 6
	sigLen       = 16
	headerLen    = 1 + 8 + idLen
	maxNameLen   = 40
)

var (
	ErrMalformed = errors.New("malformed token")
	ErrSignature = errors.New("invalid signature")
	ErrExpired   = errors.New("token expired")
)

type Token struct {
	Expires time.Time
	ID      string
	Name    string
}

func NewToken(secret []byte, ttl time.Duration, name string, now time.Time) (string, Token, error) {
	if ttl <= 0 {
		return "", Token{}, errors.New("ttl must be positive")
	}
	name = strings.TrimSpace(name)
	if len(name) > maxNameLen {
		return "", Token{}, fmt.Errorf("name too long (max %d bytes)", maxNameLen)
	}
	if !utf8.ValidString(name) {
		return "", Token{}, errors.New("name must be valid UTF-8")
	}
	payload := make([]byte, headerLen, headerLen+len(name)+sigLen)
	payload[0] = tokenVersion
	exp := now.Add(ttl).Truncate(time.Second)
	binary.BigEndian.PutUint64(payload[1:9], uint64(exp.Unix()))
	if _, err := rand.Read(payload[9:headerLen]); err != nil {
		return "", Token{}, err
	}
	payload = append(payload, name...)
	payload = append(payload, sign(secret, payload)...)
	return base64.RawURLEncoding.EncodeToString(payload),
		Token{Expires: exp, ID: hex.EncodeToString(payload[9:headerLen]), Name: name}, nil
}

// ParseToken verifies the signature first and only then reports expiry, so an
// attacker can't learn anything from a forged token.
func ParseToken(secret []byte, s string, now time.Time) (Token, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) < headerLen+sigLen || len(raw) > headerLen+maxNameLen+sigLen {
		return Token{}, ErrMalformed
	}
	payload, sig := raw[:len(raw)-sigLen], raw[len(raw)-sigLen:]
	if !hmac.Equal(sig, sign(secret, payload)) {
		return Token{}, ErrSignature
	}
	if payload[0] != tokenVersion {
		return Token{}, ErrMalformed
	}
	t := Token{
		Expires: time.Unix(int64(binary.BigEndian.Uint64(payload[1:9])), 0),
		ID:      hex.EncodeToString(payload[9:headerLen]),
		Name:    string(payload[headerLen:]),
	}
	if !now.Before(t.Expires) {
		return t, ErrExpired
	}
	return t, nil
}

func sign(secret, payload []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return m.Sum(nil)[:sigLen]
}

// ParseTTL accepts Go durations plus a "d" (day) suffix, e.g. "36h", "2d", "1d12h".
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i >= 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("invalid ttl %q", s)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = s[i+1:]
	}
	var rest time.Duration
	if s != "" {
		var err error
		if rest, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("invalid ttl: %w", err)
		}
	}
	return days + rest, nil
}
