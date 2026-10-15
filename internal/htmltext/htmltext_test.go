package htmltext

import "testing"

func TestConvert(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "paragraphs and whitespace",
			in:   "<html><head><style>p{color:red}</style><title>x</title></head><body><p>Hello   <b>world</b>,\n how are you?</p><p>Second</p></body></html>",
			want: "Hello world, how are you?\n\nSecond",
		},
		{
			name: "outlook paragraphs",
			in:   `<p class=MsoNormal>Hi team,</p><p class=MsoNormal>&nbsp;</p><p class=MsoNormal>Line one</p><p class=MsoNormal>Line two</p>`,
			want: "Hi team,\n\nLine one\nLine two",
		},
		{
			name: "gmail divs and br",
			in:   `<div>Hi</div><div><br></div><div>there<br>friend</div>`,
			want: "Hi\n\nthere\nfriend",
		},
		{
			name: "links become footnotes",
			in:   `<p>See <a href="https://example.com/a">the docs</a> and <a href="https://example.com/a">again</a> or <a href="https://b.example">https://b.example</a>, mail <a href="mailto:x@y.z">me</a>.</p>`,
			want: "See the docs[1] and again[1] or https://b.example, mail me.\n\nLinks:\n[1] https://example.com/a",
		},
		{
			name: "lists",
			in:   `<p>Todo:</p><ul><li>one</li><li>two<ol><li>a</li><li>b</li></ol></li></ul><p>done</p>`,
			want: "Todo:\n\n• one\n• two\n  1. a\n  2. b\n\ndone",
		},
		{
			name: "blockquote",
			in:   `<p>Reply</p><blockquote><p>original</p><p>text</p></blockquote>`,
			want: "Reply\n\n> original\n\n> text",
		},
		{
			name: "pre preserves layout",
			in:   "<pre>a  b\n  c</pre>",
			want: "a  b\n  c",
		},
		{
			name: "entities and images",
			in:   `<p>Fish &amp; chips &lt;3 <img src="cid:x" alt="logo"><img src="track.gif"></p>`,
			want: "Fish & chips <3 [logo]",
		},
		{
			name: "table cells",
			in:   `<table><tr><td>Name</td><td>Value</td></tr><tr><td>a</td><td>1</td></tr></table>`,
			want: "Name Value\na 1",
		},
		{
			name: "comments and conditional comments are dropped",
			in:   `<!--[if mso]><p>mso only</p><![endif]--><p>visible</p>`,
			want: "visible",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Convert(tt.in); got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}
