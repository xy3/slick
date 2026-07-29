// slick — an utterly minimal wrapper UI for sending Slack messages.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"slick/slack"
)

//go:embed web/*
var webFS embed.FS

type config struct {
	Token  string `json:"token"`  // xoxc-...
	Cookie string `json:"cookie"` // d cookie value, xoxd-...
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "slick", "config.json")
}

func loadConfig() (*config, error) {
	b, err := os.ReadFile(configPath())
	if err != nil {
		return nil, err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveConfig(c *config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600) // creds — keep them user-only
}

// server holds the live Slack client and a short cache of conversations.
type server struct {
	tmpl *template.Template

	mu       sync.RWMutex
	client   *slack.Client
	who      string // "user @ team", for the header
	convs    []slack.Conversation
	convTime time.Time
}

func (s *server) configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client != nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8383", "listen address")
	flag.Parse()

	tmpl, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}
	s := &server{tmpl: tmpl}

	// Try to bring an existing session online at startup.
	if c, err := loadConfig(); err == nil {
		if err := s.connect(c); err != nil {
			log.Printf("saved credentials rejected (%v) — will show setup", err)
		}
	}

	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/setup", s.handleSetup)
	mux.HandleFunc("/api/conversations", s.handleConversations)
	mux.HandleFunc("/api/send", s.handleSend)

	log.Printf("slick → http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// connect validates credentials and, on success, swaps in a live client.
func (s *server) connect(c *config) error {
	cl := slack.New(c.Token, c.Cookie)
	auth, err := cl.AuthTest()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.client = cl
	s.who = fmt.Sprintf("%s @ %s", auth.User, auth.Team)
	s.convs = nil
	s.mu.Unlock()
	return nil
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.configured() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.mu.RLock()
	who := s.who
	s.mu.RUnlock()
	s.render(w, "index.html", map[string]any{"Who": who})
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		c := &config{
			Token:  trim(r.FormValue("token")),
			Cookie: trim(r.FormValue("cookie")),
		}
		if err := s.connect(c); err != nil {
			s.render(w, "setup.html", map[string]any{"Error": friendly(err)})
			return
		}
		if err := saveConfig(c); err != nil {
			log.Printf("save config: %v", err)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "setup.html", nil)
}

func (s *server) handleConversations(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	client := s.client
	cached := s.convs
	fresh := time.Since(s.convTime) < 5*time.Minute
	s.mu.RUnlock()
	if client == nil {
		http.Error(w, "not configured", http.StatusUnauthorized)
		return
	}

	if cached == nil || !fresh {
		convs, err := client.Conversations()
		if err != nil {
			http.Error(w, friendly(err), http.StatusBadGateway)
			return
		}
		s.mu.Lock()
		s.convs, s.convTime = convs, time.Now()
		cached = convs
		s.mu.Unlock()
	}
	writeJSON(w, cached)
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		http.Error(w, "not configured", http.StatusUnauthorized)
		return
	}

	var body struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Channel == "" || trim(body.Text) == "" {
		http.Error(w, "channel and text required", http.StatusBadRequest)
		return
	}
	if err := client.PostMessage(body.Channel, body.Text); err != nil {
		http.Error(w, friendly(err), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func trim(s string) string { return strings.TrimSpace(s) }

// friendly turns Slack's terse error codes into a hint.
func friendly(err error) string {
	msg := err.Error()
	switch {
	case containsAny(msg, "invalid_auth", "not_authed"):
		return "Slack rejected the token or cookie — double-check both values."
	case containsAny(msg, "token_revoked", "account_inactive"):
		return "That session is no longer valid — grab a fresh token and cookie."
	default:
		return msg
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
