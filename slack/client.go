// Package slack is a tiny Slack Web API client that authenticates the way the
// browser client does: an xoxc- token plus the matching `d` cookie (xoxd-).
package slack

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiBase = "https://slack.com/api/"

// Client talks to Slack as your logged-in browser session.
type Client struct {
	token  string // xoxc-...
	cookie string // the `d` cookie value, xoxd-...
	http   *http.Client

	usersMu    sync.RWMutex
	usersCache map[string]user // id -> user, loaded lazily
}

func New(token, cookie string) *Client {
	return &Client{
		token:  token,
		cookie: cookie,
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

// call posts a form to a Slack API method and decodes the JSON into out.
func (c *Client) call(method string, form url.Values, out any) error {
	if form == nil {
		form = url.Values{}
	}
	req, err := http.NewRequest(http.MethodPost, apiBase+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.token)
	// The `d` cookie is what pairs with an xoxc- token; without it Slack
	// treats the request as unauthenticated (invalid_auth).
	req.Header.Set("Cookie", "d="+c.cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	return nil
}

type baseResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Meta  struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (b baseResp) err(method string) error {
	if !b.OK {
		return fmt.Errorf("slack %s: %s", method, b.Error)
	}
	return nil
}

// AuthTest verifies the credentials and returns the authed user/team.
type AuthTest struct {
	baseResp
	User   string `json:"user"`
	UserID string `json:"user_id"`
	Team   string `json:"team"`
	URL    string `json:"url"`
}

func (c *Client) AuthTest() (*AuthTest, error) {
	var r AuthTest
	if err := c.call("auth.test", nil, &r); err != nil {
		return nil, err
	}
	if err := r.err("auth.test"); err != nil {
		return nil, err
	}
	return &r, nil
}

// Conversation is a normalized send target for the picker.
type Conversation struct {
	ID    string `json:"id"`
	Name  string `json:"name"`  // "#general" or "@alice"
	Kind  string `json:"kind"`  // channel | private | group | dm
	Score int    `json:"-"`
}

type rawConversation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	User       string `json:"user"`
	IsChannel  bool   `json:"is_channel"`
	IsGroup    bool   `json:"is_group"`
	IsIM       bool   `json:"is_im"`
	IsMPIM     bool   `json:"is_mpim"`
	IsPrivate  bool   `json:"is_private"`
	IsArchived bool   `json:"is_archived"`
}

type user struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Deleted  bool   `json:"deleted"`
	IsBot    bool   `json:"is_bot"`
	Profile  struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

func (u user) label() string {
	switch {
	case u.Profile.DisplayName != "":
		return u.Profile.DisplayName
	case u.RealName != "":
		return u.RealName
	default:
		return u.Name
	}
}

// Conversations returns every channel, group, and DM you can post to, with
// human-readable names, ready for the picker.
func (c *Client) Conversations() ([]Conversation, error) {
	users, err := c.ensureUsers()
	if err != nil {
		return nil, err
	}

	var out []Conversation
	cursor := ""
	for {
		form := url.Values{}
		form.Set("types", "public_channel,private_channel,mpim,im")
		form.Set("exclude_archived", "true")
		form.Set("limit", "1000")
		if cursor != "" {
			form.Set("cursor", cursor)
		}

		var r struct {
			baseResp
			Channels []rawConversation `json:"channels"`
		}
		if err := c.call("conversations.list", form, &r); err != nil {
			return nil, err
		}
		if err := r.err("conversations.list"); err != nil {
			return nil, err
		}

		for _, ch := range r.Channels {
			if ch.IsArchived {
				continue
			}
			conv := Conversation{ID: ch.ID}
			switch {
			case ch.IsIM:
				u, ok := users[ch.User]
				if !ok || u.Deleted {
					continue
				}
				conv.Name = "@" + u.label()
				conv.Kind = "dm"
			case ch.IsMPIM:
				conv.Name = "@" + strings.TrimPrefix(ch.Name, "mpdm-")
				conv.Kind = "group"
			case ch.IsPrivate:
				conv.Name = "#" + ch.Name
				conv.Kind = "private"
			default:
				conv.Name = "#" + ch.Name
				conv.Kind = "channel"
			}
			out = append(out, conv)
		}

		cursor = r.Meta.NextCursor
		if cursor == "" {
			break
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ensureUsers loads the workspace directory once and caches it, so both the
// picker and message rendering can resolve user IDs to names.
func (c *Client) ensureUsers() (map[string]user, error) {
	c.usersMu.RLock()
	cached := c.usersCache
	c.usersMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	m, err := c.fetchUsers()
	if err != nil {
		return nil, err
	}
	c.usersMu.Lock()
	c.usersCache = m
	c.usersMu.Unlock()
	return m, nil
}

func (c *Client) fetchUsers() (map[string]user, error) {
	m := map[string]user{}
	cursor := ""
	for {
		form := url.Values{}
		form.Set("limit", "1000")
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var r struct {
			baseResp
			Members []user `json:"members"`
		}
		if err := c.call("users.list", form, &r); err != nil {
			return nil, err
		}
		if err := r.err("users.list"); err != nil {
			return nil, err
		}
		for _, u := range r.Members {
			m[u.ID] = u
		}
		cursor = r.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	return m, nil
}

// Message is one rendered line of conversation history.
type Message struct {
	User string `json:"user"` // author display name
	Text string `json:"text"` // rendered to plain text
	TS   string `json:"ts"`   // Slack timestamp ("1690000000.000100")
	Mine bool   `json:"mine"` // authored by the current user
}

type rawMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	User    string `json:"user"`
	BotID   string `json:"bot_id"`
	Text    string `json:"text"`
	TS      string `json:"ts"`
}

// History returns up to limit recent messages, oldest first, ready to display.
func (c *Client) History(channelID string, limit int, me string) ([]Message, error) {
	users, err := c.ensureUsers()
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("limit", strconv.Itoa(limit))
	var r struct {
		baseResp
		Messages []rawMessage `json:"messages"`
	}
	if err := c.call("conversations.history", form, &r); err != nil {
		return nil, err
	}
	if err := r.err("conversations.history"); err != nil {
		return nil, err
	}

	out := make([]Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Type != "message" || m.Subtype == "channel_join" || m.Subtype == "channel_leave" {
			continue
		}
		name := c.name(users, m.User)
		if name == "" && m.BotID != "" {
			name = "bot"
		}
		out = append(out, Message{
			User: name,
			Text: c.renderText(m.Text, users),
			TS:   m.TS,
			Mine: me != "" && m.User == me,
		})
	}
	// Slack returns newest first; flip to reading order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (c *Client) name(users map[string]user, id string) string {
	if u, ok := users[id]; ok {
		return u.label()
	}
	return ""
}

var (
	reUserMention = regexp.MustCompile(`<@([UW][A-Z0-9]+)>`)
	reChanMention = regexp.MustCompile(`<#[CG][A-Z0-9]+\|([^>]*)>`)
	reLink        = regexp.MustCompile(`<(https?://[^>|]+)(?:\|([^>]+))?>`)
)

// renderText turns Slack mrkdwn control sequences into readable plain text.
func (c *Client) renderText(s string, users map[string]user) string {
	s = reUserMention.ReplaceAllStringFunc(s, func(m string) string {
		id := reUserMention.FindStringSubmatch(m)[1]
		if n := c.name(users, id); n != "" {
			return "@" + n
		}
		return "@someone"
	})
	s = reChanMention.ReplaceAllString(s, "#$1")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		if g[2] != "" {
			return g[2]
		}
		return g[1]
	})
	return html.UnescapeString(s)
}

// PostMessage sends text to a channel/DM by ID.
func (c *Client) PostMessage(channelID, text string) error {
	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("text", text)
	var r baseResp
	if err := c.call("chat.postMessage", form, &r); err != nil {
		return err
	}
	return r.err("chat.postMessage")
}
