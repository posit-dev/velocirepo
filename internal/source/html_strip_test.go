package source

import "testing"

func TestStripHTMLContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "plain text",
			in:   "Hello, world!",
			want: "Hello, world!",
		},
		{
			name: "markdown passthrough",
			in:   "## Heading\n\nSome *bold* text.\n\n```r\ncode()\n```",
			want: "## Heading\n\nSome *bold* text.\n\n```r\ncode()\n```",
		},
		{
			name: "script removed",
			in:   "Before\n<script type=\"text/javascript\">var x = {\"data\":[1,2,3]};</script>\nAfter",
			want: "Before\n\nAfter",
		},
		{
			name: "multiline script removed",
			in:   "text\n<script>\n{\n\"vegalite\": {},\n\"data\": [1,2,3]\n}\n</script>\nmore text",
			want: "text\n\nmore text",
		},
		{
			name: "svg removed",
			in:   "text\n<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"100\"><path d=\"M0,0\"/></svg>\nmore",
			want: "text\n\nmore",
		},
		{
			name: "style removed",
			in:   "<style>.gt_table{color:red}</style><p>content</p>",
			want: "content",
		},
		{
			name: "table removed",
			in:   "<p>intro</p><table class=\"gt_table\"><tr><td style=\"color:red\">cell</td></tr></table><p>outro</p>",
			want: "intro outro",
		},
		{
			name: "generic tags stripped keep text",
			in:   "<p>Hello <strong>world</strong>!</p>",
			want: "Hello world !",
		},
		{
			name: "img with base64 src removed",
			in:   "text<img src=\"data:image/png;base64,iVBORw0KGgo=\" alt=\"chart\"/>more",
			want: "text more",
		},
		{
			name: "excessive newlines collapsed",
			in:   "line1\n\n\n\n\nline2",
			want: "line1\n\nline2",
		},
		{
			name: "case insensitive",
			in:   "<SCRIPT>bad()</SCRIPT>good",
			want: "good",
		},
		{
			name: "mixed markdown and html",
			in:   "## Heading\n\n<p>Some prose.</p>\n\n<script>vegalite()</script>\n\n```r\ncode()\n```",
			want: "## Heading\n\n Some prose.\n\n```r\ncode()\n```",
		},
		{
			name: "nested divs stripped",
			in:   "<div class=\"outer\"><div class=\"inner\"><p>text</p></div></div>",
			want: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripHTMLContent(tt.in)
			if got != tt.want {
				t.Errorf("stripHTMLContent(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}
