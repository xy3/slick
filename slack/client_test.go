package slack

import "testing"

func TestRenderText(t *testing.T) {
	c := New("t", "d")
	users := map[string]user{
		"U123": {ID: "U123", Profile: struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		}{DisplayName: "alice"}},
	}

	cases := []struct{ in, want string }{
		{"hey <@U123> ping", "hey @alice ping"},
		{"unknown <@U999>", "unknown @someone"},
		{"see <#C42|general> now", "see #general now"},
		{"link <https://x.com|the site>", "link the site"},
		{"bare <https://x.com>", "bare https://x.com"},
		{"5 &lt; 6 &amp; 7 &gt; 6", "5 < 6 & 7 > 6"},
		{"plain text", "plain text"},
	}
	for _, tc := range cases {
		if got := c.renderText(tc.in, users); got != tc.want {
			t.Errorf("renderText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
