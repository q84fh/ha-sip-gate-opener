package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	srv   *Server
	h     http.Handler
	hits  atomic.Int32
	body  atomic.Value
	fail  atomic.Bool
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{clock: time.Unix(1_800_000_000, 0)}
	ha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.body.Store(string(b))
		f.hits.Add(1)
		if f.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(ha.Close)
	f.srv = &Server{
		Secret:     testSecret,
		WebhookURL: ha.URL + "/api/webhook/x",
		Gate:       GateTiming{Opening: 25 * time.Second, Open: 10 * time.Second, Closing: 25 * time.Second},
		Revoked:    map[string]bool{},
		Location:   time.UTC,
		Lang:       langs["en"],
		IPHeader:   "X-Real-IP",
		Client:     ha.Client(),
		Now:        func() time.Time { return f.clock },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	f.h = f.srv.Handler()
	return f
}

func (f *fixture) token(t *testing.T, ttl time.Duration, name string) (string, Token) {
	tok, info, err := NewToken(testSecret, ttl, name, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	return tok, info
}

func (f *fixture) do(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	return f.send(httptest.NewRequest(method, "https://gate.example"+path, nil), hdr)
}

// api calls an API endpoint the way app.js does: JSON body with the token.
func (f *fixture) api(path, token string, hdr map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token})
	r := httptest.NewRequest("POST", "https://gate.example"+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return f.send(r, hdr)
}

func (f *fixture) send(r *http.Request, hdr map[string]string) *httptest.ResponseRecorder {
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	var r T
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("bad json %q: %v", w.Body.String(), err)
	}
	return r
}

func (f *fixture) status(t *testing.T, token string, hdr map[string]string) statusResponse {
	w := f.api("/api/status", token, hdr)
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	return decode[statusResponse](t, w)
}

func TestPage(t *testing.T) {
	f := newFixture(t)
	w := f.do("GET", "/", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `id="open"`) || !strings.Contains(body, `id="msg"`) {
		t.Fatalf("page: %d %s", w.Code, body)
	}
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("Content-Security-Policy") == "" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing security headers")
	}
	if !strings.Contains(body, "/_s/app.js?v="+f.srv.version+`"`) || len(f.srv.version) != 10 {
		t.Fatal("static assets should be versioned")
	}
	if w := f.do("GET", "/_s/app.js?v="+f.srv.version, nil); w.Code != 200 {
		t.Fatalf("static: %d", w.Code)
	}
	if w := f.do("GET", "/healthz", nil); w.Code != 200 {
		t.Fatalf("healthz: %d", w.Code)
	}

	// Tokens in the path (old link format) and other paths are not found.
	tok, _ := f.token(t, time.Hour, "")
	for _, p := range []string{"/" + tok, "/" + tok + "/open", "/api/status", "/a/b"} {
		if w := f.do("GET", p, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "Invalid link") {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
	if w := f.do("POST", "/"+tok+"/open", nil); w.Code != 404 || f.hits.Load() != 0 {
		t.Errorf("POST to old open URL: %d", w.Code)
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	tok, info := f.token(t, time.Hour, "Kasia <b>")

	r := f.status(t, tok, nil)
	if r.State != "ready" || r.Title != "Hi, Kasia <b>!" || r.Text != langs["en"].Hint || r.Gate == nil || r.Gate.Elapsed != -1 || r.Gate.Opening != 25 {
		t.Fatalf("ready: %+v %+v", r, r.Gate)
	}
	if want := "Link valid until " + info.Expires.UTC().Format(langs["en"].DateFormat); r.Footer != want {
		t.Fatalf("footer %q, want %q", r.Footer, want)
	}
	anon, _ := f.token(t, time.Hour, "")
	if r := f.status(t, anon, nil); r.Title != "Welcome!" {
		t.Fatalf("no name: %+v", r)
	}
	if f.hits.Load() != 0 {
		t.Fatal("status must never open the gate")
	}

	for _, bad := range []string{"", "garbage", tok + "x", tok[:len(tok)-1]} {
		if r := f.status(t, bad, nil); r.State != "invalid" || r.Title != "Invalid link" || r.Gate != nil {
			t.Errorf("%q: %+v", bad, r)
		}
	}

	f.clock = f.clock.Add(2 * time.Hour)
	if r := f.status(t, tok, nil); r.State != "expired" || !strings.HasPrefix(r.Text, "Kasia <b>, ") || r.Gate != nil {
		t.Fatalf("expired: %+v", r)
	}
}

func TestOpenFlow(t *testing.T) {
	f := newFixture(t)
	tok, info := f.token(t, time.Hour, "Kasia")
	sameOrigin := map[string]string{"Origin": "https://gate.example", "Sec-Fetch-Site": "same-origin", "X-Real-IP": "1.2.3.4"}
	post := func() (*httptest.ResponseRecorder, openResponse) {
		w := f.api("/api/open", tok, sameOrigin)
		return w, decode[openResponse](t, w)
	}
	elapsed := func() float64 { return f.status(t, tok, nil).Gate.Elapsed }

	if w := f.do("GET", "/api/open", sameOrigin); w.Code != 404 {
		t.Fatalf("GET open: %d", w.Code)
	}

	w, r := post()
	if w.Code != 200 || !r.OK || r.Gate == nil || r.Gate.Elapsed != 0 || r.Gate.Opening != 25 || r.Gate.Closing != 25 {
		t.Fatalf("open: %d %+v %+v", w.Code, r, r.Gate)
	}
	var payload map[string]string
	json.Unmarshal([]byte(f.body.Load().(string)), &payload)
	if f.hits.Load() != 1 || payload["name"] != "Kasia" || payload["token_id"] != info.ID || payload["ip"] != "1.2.3.4" {
		t.Fatalf("webhook: hits=%d payload=%v", f.hits.Load(), payload)
	}

	// Opening and open phases: pressing again is refused, state is reported.
	f.clock = f.clock.Add(5 * time.Second)
	w, r = post()
	if w.Code != 429 || r.RetryAfter != 30 || w.Header().Get("Retry-After") != "30" || r.Gate.Elapsed != 5 || f.hits.Load() != 1 {
		t.Fatalf("opening: %d %+v %+v", w.Code, r, r.Gate)
	}
	f.clock = f.clock.Add(29 * time.Second)
	if w, r = post(); w.Code != 429 || r.RetryAfter != 1 {
		t.Fatalf("open phase: %d %+v", w.Code, r)
	}
	if e := elapsed(); e != 34 {
		t.Fatalf("status should report the current gate cycle, got %v", e)
	}

	// Closing, 20% closed: the gate reverses, so it's as if it had been opening for 80% of 25s.
	f.clock = f.clock.Add(6 * time.Second)
	if w, r = post(); w.Code != 200 || r.Gate.Elapsed != 20 || f.hits.Load() != 2 {
		t.Fatalf("reopen while closing: %d %+v", w.Code, r.Gate)
	}
	f.clock = f.clock.Add(time.Second)
	if w, r = post(); w.Code != 429 || r.Gate.Elapsed != 21 {
		t.Fatalf("after reopen: %d %+v", w.Code, r.Gate)
	}

	// Full cycle finished -> closed.
	f.clock = f.clock.Add(39 * time.Second)
	if e := elapsed(); e != -1 {
		t.Fatalf("gate should be closed after full cycle, elapsed %v", e)
	}
	if w, r = post(); w.Code != 200 || r.Gate.Elapsed != 0 || f.hits.Load() != 3 {
		t.Fatalf("after full cycle: %d %+v", w.Code, r.Gate)
	}
}

func TestOpenRejected(t *testing.T) {
	f := newFixture(t)
	tok, info := f.token(t, time.Hour, "")

	for name, hdr := range map[string]map[string]string{
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"other origin": {"Origin": "https://evil.example"},
		"form post":    {"Content-Type": "application/x-www-form-urlencoded"},
		"text/plain":   {"Content-Type": "text/plain"},
	} {
		for _, path := range []string{"/api/open", "/api/status"} {
			if w := f.api(path, tok, hdr); w.Code != 403 {
				t.Errorf("%s %s: %d", name, path, w.Code)
			}
		}
	}
	for _, bad := range []string{"", "nope"} {
		if w := f.api("/api/open", bad, nil); w.Code != 403 || decode[openResponse](t, w).Message != langs["en"].LinkInvalid {
			t.Errorf("bad token %q: %d", bad, w.Code)
		}
	}
	big := httptest.NewRequest("POST", "https://gate.example/api/open", strings.NewReader(`{"token":"`+strings.Repeat("a", 4096)+`"}`))
	big.Header.Set("Content-Type", "application/json")
	if w := f.send(big, nil); w.Code != 403 {
		t.Errorf("oversized body: %d", w.Code)
	}

	f.srv.Revoked[info.ID] = true
	if w := f.api("/api/open", tok, nil); w.Code != 403 || !strings.Contains(decode[openResponse](t, w).Message, "expired") {
		t.Errorf("revoked: %d", w.Code)
	}
	if r := f.status(t, tok, nil); r.State != "expired" {
		t.Errorf("revoked status: %+v", r)
	}
	delete(f.srv.Revoked, info.ID)

	f.clock = f.clock.Add(time.Hour)
	if w := f.api("/api/open", tok, nil); w.Code != 403 {
		t.Errorf("expired: %d", w.Code)
	}
	if f.hits.Load() != 0 {
		t.Fatalf("webhook called %d times", f.hits.Load())
	}
}

func TestWebhookFailureDoesNotStartCycle(t *testing.T) {
	f := newFixture(t)
	tok, _ := f.token(t, time.Hour, "")
	f.fail.Store(true)
	if w := f.api("/api/open", tok, nil); w.Code != http.StatusBadGateway || decode[openResponse](t, w).OK {
		t.Fatalf("want 502, got %d", w.Code)
	}
	if e := f.status(t, tok, nil).Gate.Elapsed; e != -1 {
		t.Fatal("failed open must not start the cycle")
	}
	f.fail.Store(false)
	if w := f.api("/api/open", tok, nil); w.Code != 200 {
		t.Fatalf("retry after failure: %d", w.Code)
	}
}

func TestLanguage(t *testing.T) {
	f := newFixture(t)
	tok, _ := f.token(t, time.Hour, "Kasia")
	for _, c := range []struct{ accept, want, lang string }{
		{"pl-PL,pl;q=0.9,en;q=0.8", "Cześć, Kasia!", `lang="pl"`},
		{"de-DE,de;q=0.9,en;q=0.8", "Hi, Kasia!", `lang="en"`},
		{"", "Hi, Kasia!", `lang="en"`},
	} {
		hdr := map[string]string{"Accept-Language": c.accept}
		w := f.do("GET", "/", hdr)
		body := w.Body.String()
		if !strings.Contains(body, c.lang) || w.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("%q: want %s", c.accept, c.lang)
		}
		if !strings.Contains(body, `<script type="application/json" id="texts">{"open":`) {
			t.Errorf("%q: texts for app.js missing", c.accept)
		}
		if r := f.status(t, tok, hdr); r.Title != c.want {
			t.Errorf("%q: status title %q, want %q", c.accept, r.Title, c.want)
		}
	}

	f.srv.Lang = langs["pl"]
	if w := f.do("GET", "/nope", nil); !strings.Contains(w.Body.String(), "Nieprawidłowy link") {
		t.Error("default language should be used without Accept-Language")
	}
	w := f.api("/api/open", "nope", map[string]string{"Accept-Language": "en-GB"})
	if m := decode[openResponse](t, w).Message; m != langs["en"].LinkInvalid {
		t.Errorf("API message should follow Accept-Language, got %q", m)
	}
}

func TestLanguagesComplete(t *testing.T) {
	for code, l := range langs {
		if l.Code != code {
			t.Errorf("%s: code %q", code, l.Code)
		}
		for _, v := range []reflect.Value{reflect.ValueOf(*l), reflect.ValueOf(l.JS)} {
			for i := 0; i < v.NumField(); i++ {
				if f := v.Field(i); f.Kind() == reflect.String && f.String() == "" {
					t.Errorf("%s: %s is empty", code, v.Type().Field(i).Name)
				}
			}
		}
	}
}

func TestClientIPHeader(t *testing.T) {
	f := newFixture(t)
	tok, _ := f.token(t, time.Hour, "")
	for _, c := range []struct{ header, value, want string }{
		{"", "6.6.6.6", "192.0.2.1"},               // header not trusted
		{"X-Real-IP", "6.6.6.6", "6.6.6.6"},        // trusted
		{"X-Real-IP", "<b>not an ip", "192.0.2.1"}, // garbage falls back to the peer address
		{"X-Real-IP", " 2001:db8::1 ", "2001:db8::1"},
	} {
		f.srv.IPHeader = c.header
		f.clock = f.clock.Add(2 * time.Minute) // past the previous gate cycle
		f.body.Store("")
		f.api("/api/open", tok, map[string]string{"X-Real-IP": c.value})
		var payload map[string]string
		json.Unmarshal([]byte(f.body.Load().(string)), &payload)
		if payload["ip"] != c.want {
			t.Errorf("header %q=%q: got ip %q, want %q", c.header, c.value, payload["ip"], c.want)
		}
	}
}

func TestWebhookURLNotLogged(t *testing.T) {
	f := newFixture(t)
	var logs strings.Builder
	f.srv.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	f.srv.WebhookURL = "http://127.0.0.1:1/api/webhook/super-secret-id"
	f.srv.Client = &http.Client{Timeout: time.Second}
	tok, _ := f.token(t, time.Hour, "")
	if w := f.api("/api/open", tok, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
	if !strings.Contains(logs.String(), "webhook failed") || strings.Contains(logs.String(), "super-secret-id") {
		t.Fatalf("webhook ID must not be logged: %s", logs.String())
	}
}
