package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	Secret     []byte
	WebhookURL string
	Gate       GateTiming
	Revoked    map[string]bool
	Location   *time.Location
	Lang       *Lang  // default when Accept-Language has no supported language
	IPHeader   string // header with the client IP that the reverse proxy always sets, e.g. X-Real-IP
	Client     *http.Client
	Now        func() time.Time
	Log        *slog.Logger

	mu sync.Mutex
	// cycleStart is the (virtual) moment the gate started opening from fully closed;
	// time since it determines the gate phase. Zero when the gate is closed.
	cycleStart time.Time

	tmpl    *template.Template
	static  http.Handler
	version string
}

// GateTiming describes one full cycle: opening, staying open, closing.
// A signal while closing makes the gate open again from its current position.
type GateTiming struct {
	Opening, Open, Closing time.Duration
}

func (g GateTiming) busy() time.Duration  { return g.Opening + g.Open }
func (g GateTiming) total() time.Duration { return g.Opening + g.Open + g.Closing }

// GateState is sent to the browser, which animates the cycle locally.
// Elapsed is seconds since cycle start, or -1 when the gate is closed.
type GateState struct {
	Elapsed float64 `json:"elapsed"`
	Opening float64 `json:"opening"`
	Open    float64 `json:"open"`
	Closing float64 `json:"closing"`
}

type pageData struct {
	State   string // ready | expired | invalid
	Name    string
	Expires string
	Gate    GateState
	T       *Lang
	V       string // static assets version, for cache busting
}

// elapsedLocked returns time since cycle start, or false when the gate is closed.
func (s *Server) elapsedLocked(now time.Time) (time.Duration, bool) {
	if s.cycleStart.IsZero() {
		return 0, false
	}
	e := now.Sub(s.cycleStart)
	if e < 0 || e >= s.Gate.total() {
		return 0, false
	}
	return e, true
}

func (s *Server) gateState(now time.Time) GateState {
	s.mu.Lock()
	e, active := s.elapsedLocked(now)
	s.mu.Unlock()
	return s.stateFor(e, active)
}

func (s *Server) stateFor(e time.Duration, active bool) GateState {
	g := GateState{Elapsed: -1, Opening: s.Gate.Opening.Seconds(), Open: s.Gate.Open.Seconds(), Closing: s.Gate.Closing.Seconds()}
	if active {
		g.Elapsed = e.Seconds()
	}
	return g
}

func (s *Server) init() {
	s.tmpl = template.Must(template.ParseFS(webFS, "web/index.html"))
	sub, _ := fs.Sub(webFS, "web/static")
	h := sha256.New()
	fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(sub, p)
			h.Write(b)
		}
		return nil
	})
	s.version = hex.EncodeToString(h.Sum(nil))[:10]
	s.static = http.StripPrefix("/_s/", http.FileServer(http.FS(sub)))
}

func (s *Server) Handler() http.Handler {
	s.init()
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")

	if r.URL.Path == "/healthz" {
		w.Write([]byte("ok"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_s/") {
		h.Set("Cache-Control", "public, max-age=86400")
		s.static.ServeHTTP(w, r)
		return
	}

	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Language")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; "+
		"manifest-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.page(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "open" && r.Method == http.MethodPost:
		s.open(w, r, parts[0])
	default:
		s.render(w, r, http.StatusNotFound, pageData{State: "invalid"})
	}
}

func (s *Server) check(raw string) (Token, error) {
	t, err := ParseToken(s.Secret, raw, s.Now())
	if err == nil && s.Revoked[t.ID] {
		err = ErrExpired
	}
	return t, err
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, raw string) {
	t, err := s.check(raw)
	switch {
	case err == nil:
		s.render(w, r, http.StatusOK, pageData{
			State:   "ready",
			Name:    t.Name,
			Expires: t.Expires.In(s.Location).Format(pickLang(r, s.Lang).DateFormat),
			Gate:    s.gateState(s.Now()),
		})
	case errors.Is(err, ErrExpired):
		s.render(w, r, http.StatusGone, pageData{State: "expired", Name: t.Name})
	default:
		s.Log.Warn("invalid token", "ip", s.clientIP(r), "path_len", len(raw))
		s.render(w, r, http.StatusNotFound, pageData{State: "invalid"})
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, d pageData) {
	var buf bytes.Buffer
	d.T = pickLang(r, s.Lang)
	d.V = s.version
	if err := s.tmpl.Execute(&buf, d); err != nil {
		s.Log.Error("template", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

type openResponse struct {
	OK         bool       `json:"ok"`
	Message    string     `json:"message"`
	RetryAfter int        `json:"retry_after,omitempty"`
	Gate       *GateState `json:"gate,omitempty"`
}

func (s *Server) open(w http.ResponseWriter, r *http.Request, raw string) {
	ip := s.clientIP(r)
	l := pickLang(r, s.Lang)
	if !sameOrigin(r) {
		s.Log.Warn("cross-origin open rejected", "ip", ip, "origin", r.Header.Get("Origin"))
		writeJSON(w, http.StatusForbidden, openResponse{Message: l.Forbidden})
		return
	}
	t, err := s.check(raw)
	if err != nil {
		s.Log.Warn("open with bad token", "ip", ip, "err", err, "id", t.ID)
		msg := l.LinkInvalid
		if errors.Is(err, ErrExpired) {
			msg = l.LinkExpired
		}
		writeJSON(w, http.StatusForbidden, openResponse{Message: msg})
		return
	}

	now := s.Now()
	s.mu.Lock()
	e, active := s.elapsedLocked(now)
	if active && e < s.Gate.busy() {
		s.mu.Unlock()
		secs := int(math.Ceil((s.Gate.busy() - e).Seconds()))
		gs := s.stateFor(e, true)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, openResponse{
			Message: l.AlreadyOpen, RetryAfter: secs, Gate: &gs})
		return
	}
	start := now
	if active {
		// Re-opening while closing: the gate reverses from its current position.
		closed := float64(e-s.Gate.busy()) / float64(s.Gate.Closing)
		start = now.Add(-time.Duration((1 - closed) * float64(s.Gate.Opening)))
	}
	prev := s.cycleStart
	s.cycleStart = start
	s.mu.Unlock()

	if err := s.callWebhook(r.Context(), t, ip); err != nil {
		s.mu.Lock()
		if s.cycleStart.Equal(start) {
			s.cycleStart = prev
		}
		s.mu.Unlock()
		s.Log.Error("webhook failed", "ip", ip, "id", t.ID, "name", t.Name, "err", err)
		writeJSON(w, http.StatusBadGateway, openResponse{Message: l.WebhookFailed})
		return
	}
	s.Log.Info("gate opened", "ip", ip, "id", t.ID, "name", t.Name, "reopen", active)
	gs := s.stateFor(now.Sub(start), true)
	writeJSON(w, http.StatusOK, openResponse{OK: true, Message: l.OpeningMsg, Gate: &gs})
}

func (s *Server) callWebhook(ctx context.Context, t Token, ip string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"name": t.Name, "token_id": t.ID, "ip": ip})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errors.New("webhook returned " + resp.Status)
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// clientIP is used for logs and the webhook payload only. IPHeader must be a header
// that the reverse proxy always overwrites, otherwise clients could spoof it.
func (s *Server) clientIP(r *http.Request) string {
	if s.IPHeader != "" {
		if ip := strings.TrimSpace(r.Header.Get(s.IPHeader)); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
