package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"mime"
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
	NotFound bool
	T        *Lang
	V        string // static assets version, for cache busting
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

	// Links look like https://gate.example/#<token>. Browsers don't send the part after "#",
	// so the token never appears in request lines, access logs or Referer headers;
	// the page reads it and sends it in the body of API requests.
	switch {
	case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.render(w, r, http.StatusOK, pageData{})
	case r.URL.Path == "/api/status" && r.Method == http.MethodPost:
		s.status(w, r)
	case r.URL.Path == "/api/open" && r.Method == http.MethodPost:
		s.open(w, r)
	default:
		s.render(w, r, http.StatusNotFound, pageData{NotFound: true})
	}
}

func (s *Server) check(raw string) (Token, error) {
	t, err := ParseToken(s.Secret, raw, s.Now())
	if err == nil && s.Revoked[t.ID] {
		err = ErrExpired
	}
	return t, err
}

// apiRequest checks that an API call comes from our own page and returns the token from its JSON body.
// Requiring application/json also makes browsers send a CORS preflight for cross-site requests.
func (s *Server) apiRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !sameOrigin(r) || ct != "application/json" {
		s.Log.Warn("cross-origin request rejected", "ip", s.clientIP(r), "path", r.URL.Path, "origin", r.Header.Get("Origin"))
		return "", false
	}
	var req struct {
		Token string `json:"token"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req)
	return req.Token, true
}

// statusResponse tells the page what to show; texts are already localized.
type statusResponse struct {
	State  string     `json:"state"` // ready | expired | invalid
	Title  string     `json:"title"`
	Text   string     `json:"text"`
	Footer string     `json:"footer,omitempty"`
	Gate   *GateState `json:"gate,omitempty"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	l := pickLang(r, s.Lang)
	raw, ok := s.apiRequest(w, r)
	if !ok {
		writeJSON(w, http.StatusForbidden, statusResponse{State: "invalid", Title: l.InvalidTitle, Text: l.Forbidden})
		return
	}
	t, err := s.check(raw)
	switch {
	case err == nil:
		resp := statusResponse{State: "ready", Title: l.Welcome, Text: l.Hint,
			Footer: fmt.Sprintf(l.ValidUntil, t.Expires.In(s.Location).Format(l.DateFormat))}
		if t.Name != "" {
			resp.Title = fmt.Sprintf(l.Hello, t.Name)
		}
		gs := s.gateState(s.Now())
		resp.Gate = &gs
		writeJSON(w, http.StatusOK, resp)
	case errors.Is(err, ErrExpired):
		resp := statusResponse{State: "expired", Title: l.ExpiredTitle, Text: l.Expired}
		if t.Name != "" {
			resp.Text = fmt.Sprintf(l.ExpiredNamed, t.Name)
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		s.Log.Warn("invalid token", "ip", s.clientIP(r), "token_len", len(raw))
		writeJSON(w, http.StatusOK, statusResponse{State: "invalid", Title: l.InvalidTitle, Text: l.Invalid})
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

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	l := pickLang(r, s.Lang)
	raw, ok := s.apiRequest(w, r)
	if !ok {
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
		// *url.Error includes the full URL; the webhook ID in it is a secret, keep it out of logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
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
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(s.IPHeader))); ip != nil {
			return ip.String()
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
