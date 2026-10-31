package main

import (
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
	r := httptest.NewRequest(method, "https://gate.example"+path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) openResponse {
	var r openResponse
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("bad json %q: %v", w.Body.String(), err)
	}
	return r
}

func TestPageStates(t *testing.T) {
	f := newFixture(t)
	tok, _ := f.token(t, time.Hour, "Kasia <b>")

	w := f.do("GET", "/"+tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Hi, Kasia &lt;b&gt;!") || !strings.Contains(w.Body.String(), `id="open"`) {
		t.Fatalf("ready page: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing security headers")
	}
	if f.hits.Load() != 0 {
		t.Fatal("GET must never open the gate")
	}

	f.clock = f.clock.Add(2 * time.Hour)
	if w := f.do("GET", "/"+tok, nil); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "Link expired") {
		t.Fatalf("expired page: %d", w.Code)
	}
	for _, p := range []string{"/", "/garbage", "/a/b/c", "/" + tok + "x"} {
		if w := f.do("GET", p, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "Invalid link") {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if !strings.Contains(w.Body.String(), "/_s/app.js?v="+f.srv.version+`"`) || len(f.srv.version) != 10 {
		t.Fatal("static assets should be versioned")
	}
	if w := f.do("GET", "/_s/app.js?v="+f.srv.version, nil); w.Code != 200 {
		t.Fatalf("static: %d", w.Code)
	}
	if w := f.do("GET", "/healthz", nil); w.Code != 200 {
		t.Fatalf("healthz: %d", w.Code)
	}
}

func TestOpenFlow(t *testing.T) {
	f := newFixture(t)
	tok, info := f.token(t, time.Hour, "Kasia")
	sameOrigin := map[string]string{"Origin": "https://gate.example", "Sec-Fetch-Site": "same-origin", "X-Real-IP": "1.2.3.4"}
	post := func() (*httptest.ResponseRecorder, openResponse) {
		w := f.do("POST", "/"+tok+"/open", sameOrigin)
		return w, decode(t, w)
	}

	if w := f.do("GET", "/"+tok+"/open", sameOrigin); w.Code != 404 {
		t.Fatalf("GET open: %d", w.Code)
	}
	if w := f.do("GET", "/"+tok, nil); !strings.Contains(w.Body.String(), `data-elapsed="-1"`) ||
		!strings.Contains(w.Body.String(), `data-opening="25"`) {
		t.Fatal("closed gate should render elapsed=-1 and timings")
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
	if w := f.do("GET", "/"+tok, nil); !strings.Contains(w.Body.String(), `data-elapsed="34"`) {
		t.Fatal("page should render current gate cycle")
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
	if w := f.do("GET", "/"+tok, nil); !strings.Contains(w.Body.String(), `data-elapsed="-1"`) {
		t.Fatal("gate should be closed after full cycle")
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
	} {
		if w := f.do("POST", "/"+tok+"/open", hdr); w.Code != 403 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := f.do("POST", "/nope/open", nil); w.Code != 403 {
		t.Errorf("bad token: %d", w.Code)
	}

	f.srv.Revoked[info.ID] = true
	if w := f.do("POST", "/"+tok+"/open", nil); w.Code != 403 || !strings.Contains(decode(t, w).Message, "expired") {
		t.Errorf("revoked: %d", w.Code)
	}
	if w := f.do("GET", "/"+tok, nil); w.Code != http.StatusGone {
		t.Errorf("revoked page: %d", w.Code)
	}
	delete(f.srv.Revoked, info.ID)

	f.clock = f.clock.Add(time.Hour)
	if w := f.do("POST", "/"+tok+"/open", nil); w.Code != 403 {
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
	if w := f.do("POST", "/"+tok+"/open", nil); w.Code != http.StatusBadGateway || decode(t, w).OK {
		t.Fatalf("want 502, got %d", w.Code)
	}
	if w := f.do("GET", "/"+tok, nil); !strings.Contains(w.Body.String(), `data-elapsed="-1"`) {
		t.Fatal("failed open must not start the cycle")
	}
	f.fail.Store(false)
	if w := f.do("POST", "/"+tok+"/open", nil); w.Code != 200 {
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
		w := f.do("GET", "/"+tok, map[string]string{"Accept-Language": c.accept})
		body := w.Body.String()
		if !strings.Contains(body, c.want) || !strings.Contains(body, c.lang) || w.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("%q: want %q and %s", c.accept, c.want, c.lang)
		}
		if !strings.Contains(body, `<script type="application/json" id="texts">{"open":`) {
			t.Errorf("%q: texts for app.js missing", c.accept)
		}
	}

	f.srv.Lang = langs["pl"]
	if w := f.do("GET", "/nope", nil); !strings.Contains(w.Body.String(), "Nieprawidłowy link") {
		t.Error("default language should be used without Accept-Language")
	}
	w := f.do("POST", "/nope/open", map[string]string{"Accept-Language": "en-GB"})
	if m := decode(t, w).Message; m != langs["en"].LinkInvalid {
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
	f.srv.IPHeader = ""
	f.do("POST", "/"+tok+"/open", map[string]string{"X-Real-IP": "6.6.6.6"})
	var payload map[string]string
	json.Unmarshal([]byte(f.body.Load().(string)), &payload)
	if payload["ip"] != "192.0.2.1" {
		t.Fatalf("untrusted header must be ignored, got %q", payload["ip"])
	}
}
