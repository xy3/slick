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

// Segment is one piece of a rendered message: plain text, a link, inline code,
// or a fenced code block. The client builds DOM nodes from these, so message
// content never has to be trusted as HTML.
type Segment struct {
	Type string `json:"type"`          // text | link | code | pre
	Text string `json:"text"`          // display text (code body for pre/code)
	URL  string `json:"url,omitempty"` // for link segments only; always http(s)
}

// Message is one line of conversation history, rendered into safe segments.
type Message struct {
	User string    `json:"user"` // author display name
	Segs []Segment `json:"segs"` // rendered message body
	TS   string    `json:"ts"`   // Slack timestamp ("1690000000.000100")
	Mine bool      `json:"mine"` // authored by the current user
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
			Segs: c.renderSegments(m.Text, users),
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
	reCodeBlock   = regexp.MustCompile("(?s)```(.*?)```")
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
)

// renderSegments turns Slack mrkdwn into a flat list of display segments:
// fenced code blocks, inline code, links, and plain text (with mentions and
// channel references resolved to readable names).
func (c *Client) renderSegments(s string, users map[string]user) []Segment {
	var segs []Segment
	last := 0
	for _, m := range reCodeBlock.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = append(segs, c.inlineSegments(s[last:m[0]], users)...)
		}
		code := strings.Trim(s[m[2]:m[3]], "\n")
		segs = append(segs, Segment{Type: "pre", Text: html.UnescapeString(code)})
		last = m[1]
	}
	if last < len(s) {
		segs = append(segs, c.inlineSegments(s[last:], users)...)
	}
	return segs
}

// inlineSegments splits a non-fenced span into inline-code and rich-text runs.
func (c *Client) inlineSegments(s string, users map[string]user) []Segment {
	var segs []Segment
	last := 0
	for _, m := range reInlineCode.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = append(segs, c.textAndLinks(s[last:m[0]], users)...)
		}
		segs = append(segs, Segment{Type: "code", Text: html.UnescapeString(s[m[2]:m[3]])})
		last = m[1]
	}
	if last < len(s) {
		segs = append(segs, c.textAndLinks(s[last:], users)...)
	}
	return segs
}

// textAndLinks resolves mentions/channels and pulls out <url|label> links,
// yielding text and link segments.
func (c *Client) textAndLinks(s string, users map[string]user) []Segment {
	s = reUserMention.ReplaceAllStringFunc(s, func(m string) string {
		id := reUserMention.FindStringSubmatch(m)[1]
		if n := c.name(users, id); n != "" {
			return "@" + n
		}
		return "@someone"
	})
	s = reChanMention.ReplaceAllString(s, "#$1")

	var segs []Segment
	last := 0
	for _, m := range reLink.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = appendText(segs, s[last:m[0]])
		}
		link := s[m[2]:m[3]]
		text := link
		if m[4] >= 0 { // captured label
			text = s[m[4]:m[5]]
		}
		segs = append(segs, Segment{
			Type: "link",
			Text: html.UnescapeString(text),
			URL:  html.UnescapeString(link),
		})
		last = m[1]
	}
	if last < len(s) {
		segs = appendText(segs, s[last:])
	}
	return segs
}

// appendText adds a text segment, merging into a trailing text run so adjacent
// plain spans collapse into one node.
func appendText(segs []Segment, raw string) []Segment {
	t := html.UnescapeString(raw)
	if t == "" {
		return segs
	}
	if n := len(segs); n > 0 && segs[n-1].Type == "text" {
		segs[n-1].Text += t
		return segs
	}
	return append(segs, Segment{Type: "text", Text: t})
}

// Count is an unread signal for one conversation.
type Count struct {
	ID       string `json:"id"`
	Mentions int    `json:"mentions"` // messages that @-mention you
	Unread   bool   `json:"unread"`   // has any unread messages
}

type rawCount struct {
	ID           string `json:"id"`
	HasUnreads   bool   `json:"has_unreads"`
	MentionCount int    `json:"mention_count"`
}

// Counts asks Slack (the same client.counts endpoint the web app uses) which
// conversations have unread messages or mentions. Only conversations with
// something to show are returned.
func (c *Client) Counts() ([]Count, error) {
	var r struct {
		baseResp
		Channels []rawCount `json:"channels"`
		MPIMs    []rawCount `json:"mpims"`
		IMs      []rawCount `json:"ims"`
	}
	if err := c.call("client.counts", nil, &r); err != nil {
		return nil, err
	}
	if err := r.err("client.counts"); err != nil {
		return nil, err
	}

	var out []Count
	for _, group := range [][]rawCount{r.Channels, r.MPIMs, r.IMs} {
		for _, x := range group {
			if x.HasUnreads || x.MentionCount > 0 {
				out = append(out, Count{ID: x.ID, Mentions: x.MentionCount, Unread: x.HasUnreads})
			}
		}
	}
	return out, nil
}

// Mark moves the read cursor for a conversation up to ts, clearing its unread
// state on Slack (the same thing the web client does when you view a channel).
func (c *Client) Mark(channelID, ts string) error {
	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("ts", ts)
	var r baseResp
	if err := c.call("conversations.mark", form, &r); err != nil {
		return err
	}
	return r.err("conversations.mark")
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
