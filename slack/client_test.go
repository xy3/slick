package slack

import (
	"reflect"
	"testing"
)

func TestRenderSegments(t *testing.T) {
	c := New("t", "d")
	users := map[string]user{
		"U123": {ID: "U123", Profile: struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		}{DisplayName: "alice"}},
	}

	cases := []struct {
		name string
		in   string
		want []Segment
	}{
		{"mention resolved", "hey <@U123> ping", []Segment{{Type: "text", Text: "hey @alice ping"}}},
		{"unknown mention", "yo <@U999>", []Segment{{Type: "text", Text: "yo @someone"}}},
		{"channel ref", "see <#C42|general> now", []Segment{{Type: "text", Text: "see #general now"}}},
		{"labelled link", "read <https://x.com|the site> ok", []Segment{
			{Type: "text", Text: "read "},
			{Type: "link", Text: "the site", URL: "https://x.com"},
			{Type: "text", Text: " ok"},
		}},
		{"bare link", "go <https://x.com>", []Segment{
			{Type: "text", Text: "go "},
			{Type: "link", Text: "https://x.com", URL: "https://x.com"},
		}},
		{"entities", "5 &lt; 6 &amp; 7", []Segment{{Type: "text", Text: "5 < 6 & 7"}}},
		{"inline code", "use `go vet` here", []Segment{
			{Type: "text", Text: "use "},
			{Type: "code", Text: "go vet"},
			{Type: "text", Text: " here"},
		}},
		{"code block", "before ```\nfmt.Println(x)\n``` after", []Segment{
			{Type: "text", Text: "before "},
			{Type: "pre", Text: "fmt.Println(x)"},
			{Type: "text", Text: " after"},
		}},
		{"entities in code", "```\na &lt; b\n```", []Segment{{Type: "pre", Text: "a < b"}}},
		{"plain", "plain text", []Segment{{Type: "text", Text: "plain text"}}},
	}
	for _, tc := range cases {
		if got := c.renderSegments(tc.in, users); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: renderSegments(%q) = %#v, want %#v", tc.name, tc.in, got, tc.want)
		}
	}
}
