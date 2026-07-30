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

// Segment is one piece of a rendered message: plain text, a mention, a link,
// inline code, a fenced code block, or an image. The client builds DOM nodes
// from these, so message content never has to be trusted as HTML.
type Segment struct {
	Type  string `json:"type"`            // text | mention | link | code | pre | image
	Text  string `json:"text"`            // display text (code body for pre/code, alt for image)
	URL   string `json:"url,omitempty"`   // link target, or image thumbnail src
	Href  string `json:"href,omitempty"`  // image: full-size link target
	Style string `json:"style,omitempty"` // text emphasis: b | i | s
	Self  bool   `json:"self,omitempty"`  // mention: refers to the current user (or @here/@channel)
}

// Message is one line of conversation history, rendered into safe segments.
type Message struct {
	User    string    `json:"user"`              // author display name
	Segs    []Segment `json:"segs"`              // rendered message body
	TS      string    `json:"ts"`                // Slack timestamp ("1690000000.000100")
	Mine    bool      `json:"mine"`              // authored by the current user
	Thread  string    `json:"thread,omitempty"`  // parent ts, if this message has a thread
	Replies int       `json:"replies,omitempty"` // reply count, for thread parents
}

type rawMessage struct {
	Type       string    `json:"type"`
	Subtype    string    `json:"subtype"`
	User       string    `json:"user"`
	BotID      string    `json:"bot_id"`
	Text       string    `json:"text"`
	TS         string    `json:"ts"`
	ThreadTS   string    `json:"thread_ts"`
	ReplyCount int       `json:"reply_count"`
	Files      []rawFile `json:"files"`
}

type rawFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mimetype    string `json:"mimetype"`
	URLPrivate  string `json:"url_private"`
	Thumb360    string `json:"thumb_360"`
	Thumb720    string `json:"thumb_720"`
	Thumb360Gif string `json:"thumb_360_gif"` // animated thumbnails, present for GIFs
	Thumb480Gif string `json:"thumb_480_gif"`
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
		out = append(out, c.buildMessage(m, users, me))
	}
	// Slack returns newest first; flip to reading order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Replies returns a thread: the parent message followed by its replies, oldest
// first, ready to display.
func (c *Client) Replies(channelID, threadTS, me string) ([]Message, error) {
	users, err := c.ensureUsers()
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("ts", threadTS)
	form.Set("limit", "50")
	var r struct {
		baseResp
		Messages []rawMessage `json:"messages"`
	}
	if err := c.call("conversations.replies", form, &r); err != nil {
		return nil, err
	}
	if err := r.err("conversations.replies"); err != nil {
		return nil, err
	}

	out := make([]Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Type != "message" {
			continue
		}
		out = append(out, c.buildMessage(m, users, me))
	}
	return out, nil // already oldest-first
}

// buildMessage renders one raw message (text, images, thread info) into the
// display shape shared by history and thread views.
func (c *Client) buildMessage(m rawMessage, users map[string]user, me string) Message {
	name := c.name(users, m.User)
	if name == "" && m.BotID != "" {
		name = "bot"
	}

	segs := c.renderSegments(m.Text, users, me)
	for _, f := range m.Files {
		if !strings.HasPrefix(f.Mimetype, "image/") || f.URLPrivate == "" {
			continue
		}
		src := f.Thumb720
		if src == "" {
			src = f.Thumb360
		}
		// GIFs animate only via the animated thumbnail (or the original); the
		// static thumb_* frames would freeze on the first frame.
		if f.Mimetype == "image/gif" {
			switch {
			case f.Thumb480Gif != "":
				src = f.Thumb480Gif
			case f.Thumb360Gif != "":
				src = f.Thumb360Gif
			default:
				src = f.URLPrivate
			}
		}
		if src == "" {
			src = f.URLPrivate
		}
		// URLs are raw Slack file links; the browser routes them through slick's
		// authenticated /api/file proxy (they can't be fetched unauthenticated).
		segs = append(segs, Segment{Type: "image", Text: f.Name, URL: src, Href: f.URLPrivate})
	}

	msg := Message{
		User: name,
		Segs: segs,
		TS:   m.TS,
		Mine: me != "" && m.User == me,
	}
	// A parent message carries a reply count; surface it so the UI can offer to
	// open the thread.
	if m.ReplyCount > 0 {
		msg.Thread = m.TS
		msg.Replies = m.ReplyCount
	}
	return msg
}

func (c *Client) name(users map[string]user, id string) string {
	if u, ok := users[id]; ok {
		return u.label()
	}
	return ""
}

var (
	reCodeBlock  = regexp.MustCompile("(?s)```(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	// Slack wraps mentions, channels, and links in angle brackets; literal `<`
	// in user text arrives escaped as &lt;, so any real `<…>` is a Slack token.
	reAngle = regexp.MustCompile(`<([^>]+)>`)
	// Emphasis: *bold*, ~strike~, _italic_ (underscore boundaries checked below).
	reEmph = regexp.MustCompile(`(?s)\*(\S(?:[^*\n]*?\S)?)\*|~(\S(?:[^~\n]*?\S)?)~|_(\S(?:[^_\n]*?\S)?)_`)
)

// renderSegments turns Slack mrkdwn into a flat list of display segments:
// fenced code blocks, inline code, links, mentions, emphasis, and plain text
// (mentions and channel references resolved to readable names).
func (c *Client) renderSegments(s string, users map[string]user, me string) []Segment {
	var segs []Segment
	last := 0
	for _, m := range reCodeBlock.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = append(segs, c.inlineSegments(s[last:m[0]], users, me)...)
		}
		code := strings.Trim(s[m[2]:m[3]], "\n")
		segs = append(segs, Segment{Type: "pre", Text: html.UnescapeString(code)})
		last = m[1]
	}
	if last < len(s) {
		segs = append(segs, c.inlineSegments(s[last:], users, me)...)
	}
	return segs
}

// inlineSegments splits a non-fenced span into inline-code and rich-text runs.
func (c *Client) inlineSegments(s string, users map[string]user, me string) []Segment {
	var segs []Segment
	last := 0
	for _, m := range reInlineCode.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = append(segs, c.richText(s[last:m[0]], users, me)...)
		}
		segs = append(segs, Segment{Type: "code", Text: html.UnescapeString(s[m[2]:m[3]])})
		last = m[1]
	}
	if last < len(s) {
		segs = append(segs, c.richText(s[last:], users, me)...)
	}
	return segs
}

// richText pulls Slack angle-bracket tokens (mentions, channels, links) out of a
// span and runs emphasis parsing over the plain text between them.
func (c *Client) richText(s string, users map[string]user, me string) []Segment {
	var segs []Segment
	last := 0
	for _, m := range reAngle.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			segs = appendEmph(segs, s[last:m[0]])
		}
		segs = append(segs, c.angleSegment(s[m[2]:m[3]], users, me))
		last = m[1]
	}
	if last < len(s) {
		segs = appendEmph(segs, s[last:])
	}
	return segs
}

// angleSegment renders a single Slack `<…>` token into a mention or link segment.
func (c *Client) angleSegment(inner string, users map[string]user, me string) Segment {
	switch inner[0] {
	case '@': // user mention: <@U123> or <@U123|label>
		id := inner[1:]
		if i := strings.IndexByte(id, '|'); i >= 0 {
			id = id[:i]
		}
		name := c.name(users, id)
		if name == "" {
			name = "someone"
		}
		return Segment{Type: "mention", Text: "@" + name, Self: id == me}
	case '#': // channel: <#C42|general>
		label := inner[1:]
		if i := strings.IndexByte(label, '|'); i >= 0 {
			label = label[i+1:]
		}
		return Segment{Type: "mention", Text: "#" + label}
	case '!': // broadcast: <!here>, <!channel>, <!everyone>, <!subteam^ID|@grp>
		rest := inner[1:]
		if i := strings.IndexByte(rest, '|'); i >= 0 { // named group carries its label
			return Segment{Type: "mention", Text: "@" + strings.TrimPrefix(rest[i+1:], "@"), Self: true}
		}
		return Segment{Type: "mention", Text: "@" + rest, Self: true}
	default: // link: <https://x|label> or bare <https://x>
		link, text := inner, inner
		if i := strings.IndexByte(inner, '|'); i >= 0 {
			link, text = inner[:i], inner[i+1:]
		}
		return Segment{Type: "link", Text: html.UnescapeString(text), URL: html.UnescapeString(link)}
	}
}

// appendEmph unescapes a plain span, splits it into emphasis-styled text runs,
// and appends them (coalescing adjacent unstyled runs into one node).
func appendEmph(segs []Segment, raw string) []Segment {
	t := html.UnescapeString(raw)
	if t == "" {
		return segs
	}
	last := 0
	for _, m := range reEmph.FindAllStringSubmatchIndex(t, -1) {
		style, g := "b", 2
		switch {
		case m[4] >= 0:
			style, g = "s", 4
		case m[6] >= 0:
			style, g = "i", 6
		}
		// Underscore italic must sit on word boundaries, so file_names_like_this
		// aren't mangled into italics.
		if style == "i" && ((m[0] > 0 && isWordByte(t[m[0]-1])) || (m[1] < len(t) && isWordByte(t[m[1]]))) {
			continue
		}
		if m[0] > last {
			segs = appendText(segs, t[last:m[0]])
		}
		segs = append(segs, Segment{Type: "text", Text: t[m[g]:m[g+1]], Style: style})
		last = m[1]
	}
	if last < len(t) {
		segs = appendText(segs, t[last:])
	}
	return segs
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// appendText adds an unstyled text segment, merging into a trailing plain run so
// adjacent spans collapse into one node.
func appendText(segs []Segment, t string) []Segment {
	if t == "" {
		return segs
	}
	if n := len(segs); n > 0 && segs[n-1].Type == "text" && segs[n-1].Style == "" {
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

// PostMessage sends text to a channel/DM by ID. If threadTS is non-empty the
// message is posted as a reply in that thread.
func (c *Client) PostMessage(channelID, text, threadTS string) error {
	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("text", text)
	if threadTS != "" {
		form.Set("thread_ts", threadTS)
	}
	var r baseResp
	if err := c.call("chat.postMessage", form, &r); err != nil {
		return err
	}
	return r.err("chat.postMessage")
}

// FetchFile does an authenticated GET against a Slack file URL (url_private or
// a thumbnail). Slack file URLs require the same token+cookie as the API, so
// the browser can't load them directly — slick proxies them through here.
func (c *Client) FetchFile(rawurl string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Cookie", "d="+c.cookie)
	return c.http.Do(req)
}
