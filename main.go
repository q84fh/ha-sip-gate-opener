package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
)

const usage = `Usage:
  gate-opener [serve]                        run the HTTP server
  gate-opener token [-ttl 24h] [-name Ann]   create a guest link (ttl e.g. 90m, 12h, 2d, 1d12h)
  gate-opener inspect <token|link>           show link details
  gate-opener version                        print the version

Environment:
  GATE_SECRET        HMAC key, base64, at least 32 bytes (required)
  GATE_WEBHOOK_URL   Home Assistant webhook URL (required for serve)
  GATE_BASE_URL      public URL used by "token" (default http://localhost:8080)
  GATE_OPENING       how long the gate takes to open (default 25s)
  GATE_OPEN_HOLD     how long it stays open (default 10s)
  GATE_CLOSING       how long it takes to close (default 25s)
  GATE_REVOKED       comma-separated IDs of revoked links
  GATE_LANG          page language when the browser's is not supported: en, pl (default en)
  GATE_IP_HEADER     header with the client IP set by your reverse proxy, e.g. X-Real-IP
                     (default: none, the TCP peer address is used)
  GATE_LISTEN        listen address (default :8080)
  TZ                 time zone for dates shown to guests, e.g. Europe/Warsaw (default UTC)
`

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "token":
		err = tokenCmd(args)
	case "inspect":
		err = inspectCmd(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadSecret() ([]byte, error) {
	s := strings.TrimSpace(os.Getenv("GATE_SECRET"))
	if s == "" {
		return nil, errors.New("GATE_SECRET is not set")
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("GATE_SECRET is not valid base64: %w", err)
	}
	if len(key) < 32 {
		return nil, errors.New("GATE_SECRET must be at least 32 bytes")
	}
	return key, nil
}

func serve() error {
	secret, err := loadSecret()
	if err != nil {
		return err
	}
	webhook := os.Getenv("GATE_WEBHOOK_URL")
	if webhook == "" {
		return errors.New("GATE_WEBHOOK_URL is not set")
	}
	var gate GateTiming
	for _, d := range []struct {
		key, def string
		dst      *time.Duration
	}{
		{"GATE_OPENING", "25s", &gate.Opening},
		{"GATE_OPEN_HOLD", "10s", &gate.Open},
		{"GATE_CLOSING", "25s", &gate.Closing},
	} {
		v, err := time.ParseDuration(env(d.key, d.def))
		if err != nil || v <= 0 {
			return fmt.Errorf("%s: invalid duration %q", d.key, env(d.key, d.def))
		}
		*d.dst = v
	}
	lang, ok := langs[env("GATE_LANG", "en")]
	if !ok {
		return fmt.Errorf("GATE_LANG: unsupported language %q", os.Getenv("GATE_LANG"))
	}
	revoked := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("GATE_REVOKED"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			revoked[id] = true
		}
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Server{
		Secret:     secret,
		WebhookURL: webhook,
		Gate:       gate,
		Revoked:    revoked,
		Location:   time.Local,
		Lang:       lang,
		IPHeader:   os.Getenv("GATE_IP_HEADER"),
		Client:     &http.Client{Timeout: 15 * time.Second},
		Now:        time.Now,
		Log:        log,
	}
	srv := &http.Server{
		Addr:              env("GATE_LISTEN", ":8080"),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "version", version, "addr", srv.Addr, "opening", gate.Opening.String(), "open", gate.Open.String(), "closing", gate.Closing.String(), "revoked", len(revoked), "lang", lang.Code, "tz", time.Local.String())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func tokenCmd(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	ttlStr := fs.String("ttl", "24h", "link validity, e.g. 90m, 12h, 2d")
	name := fs.String("name", "", "guest name (shown on the page, in logs and sent to Home Assistant)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	secret, err := loadSecret()
	if err != nil {
		return err
	}
	ttl, err := ParseTTL(*ttlStr)
	if err != nil {
		return err
	}
	tok, t, err := NewToken(secret, ttl, *name, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("%s/%s\n", strings.TrimRight(env("GATE_BASE_URL", "http://localhost:8080"), "/"), tok)
	fmt.Fprintf(os.Stderr, "Valid until: %s, ID: %s\n", t.Expires.Format("2006-01-02 15:04 MST"), t.ID)
	return nil
}

func inspectCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("give a token or a link")
	}
	secret, err := loadSecret()
	if err != nil {
		return err
	}
	raw := args[0]
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	t, err := ParseToken(secret, raw, time.Now())
	status := "valid"
	switch {
	case errors.Is(err, ErrExpired):
		status = "expired"
	case err != nil:
		return err
	}
	for _, id := range strings.Split(os.Getenv("GATE_REVOKED"), ",") {
		if strings.TrimSpace(id) == t.ID {
			status = "revoked"
		}
	}
	fmt.Printf("ID: %s\nName: %s\nValid until: %s\nStatus: %s\n", t.ID, t.Name, t.Expires.Format("2006-01-02 15:04 MST"), status)
	return nil
}
