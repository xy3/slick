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
		me   string
		want []Segment
	}{
		{"mention resolved", "hey <@U123> ping", "", []Segment{
			{Type: "text", Text: "hey "},
			{Type: "mention", Text: "@alice"},
			{Type: "text", Text: " ping"},
		}},
		{"self mention", "cc <@U123>", "U123", []Segment{
			{Type: "text", Text: "cc "},
			{Type: "mention", Text: "@alice", Self: true},
		}},
		{"unknown mention", "yo <@U999>", "", []Segment{
			{Type: "text", Text: "yo "},
			{Type: "mention", Text: "@someone"},
		}},
		{"broadcast here", "ping <!here> all", "", []Segment{
			{Type: "text", Text: "ping "},
			{Type: "mention", Text: "@here", Self: true},
			{Type: "text", Text: " all"},
		}},
		{"channel ref", "see <#C42|general> now", "", []Segment{
			{Type: "text", Text: "see "},
			{Type: "mention", Text: "#general"},
			{Type: "text", Text: " now"},
		}},
		{"labelled link", "read <https://x.com|the site> ok", "", []Segment{
			{Type: "text", Text: "read "},
			{Type: "link", Text: "the site", URL: "https://x.com"},
			{Type: "text", Text: " ok"},
		}},
		{"bare link", "go <https://x.com>", "", []Segment{
			{Type: "text", Text: "go "},
			{Type: "link", Text: "https://x.com", URL: "https://x.com"},
		}},
		{"bold", "make it *pop* now", "", []Segment{
			{Type: "text", Text: "make it "},
			{Type: "text", Text: "pop", Style: "b"},
			{Type: "text", Text: " now"},
		}},
		{"italic", "so _very_ nice", "", []Segment{
			{Type: "text", Text: "so "},
			{Type: "text", Text: "very", Style: "i"},
			{Type: "text", Text: " nice"},
		}},
		{"strike", "no ~bad~ ideas", "", []Segment{
			{Type: "text", Text: "no "},
			{Type: "text", Text: "bad", Style: "s"},
			{Type: "text", Text: " ideas"},
		}},
		{"underscore not italic", "file_name_here", "", []Segment{{Type: "text", Text: "file_name_here"}}},
		{"entities", "5 &lt; 6 &amp; 7", "", []Segment{{Type: "text", Text: "5 < 6 & 7"}}},
		{"inline code", "use `go vet` here", "", []Segment{
			{Type: "text", Text: "use "},
			{Type: "code", Text: "go vet"},
			{Type: "text", Text: " here"},
		}},
		{"code block", "before ```\nfmt.Println(x)\n``` after", "", []Segment{
			{Type: "text", Text: "before "},
			{Type: "pre", Text: "fmt.Println(x)"},
			{Type: "text", Text: " after"},
		}},
		{"entities in code", "```\na &lt; b\n```", "", []Segment{{Type: "pre", Text: "a < b"}}},
		{"plain", "plain text", "", []Segment{{Type: "text", Text: "plain text"}}},
		{"emoji shortcode", "nice :fire: work :+1:", "", []Segment{
			{Type: "text", Text: "nice \U0001F525 work \U0001F44D"},
		}},
		{"unknown emoji shortcode left literal", "custom :my_custom_emoji:", "", []Segment{
			{Type: "text", Text: "custom :my_custom_emoji:"},
		}},
		{"emoji not expanded in code", "run `git :fire:` now", "", []Segment{
			{Type: "text", Text: "run "},
			{Type: "code", Text: "git :fire:"},
			{Type: "text", Text: " now"},
		}},
		{"emoji inside emphasis", "so *:fire:* hot", "", []Segment{
			{Type: "text", Text: "so "},
			{Type: "text", Text: "\U0001F525", Style: "b"},
			{Type: "text", Text: " hot"},
		}},
	}
	for _, tc := range cases {
		if got := c.renderSegments(tc.in, users, tc.me); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: renderSegments(%q) = %#v, want %#v", tc.name, tc.in, got, tc.want)
		}
	}
}
