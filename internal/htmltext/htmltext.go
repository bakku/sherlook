// Package htmltext converts HTML mail bodies to readable plain text.
// Links become numbered footnotes; layout is reduced to paragraphs, lists
// and quote prefixes. Line wrapping is left to the caller.
package htmltext

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Convert renders HTML as plain text.
func Convert(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	c := &conv{linkIndex: map[string]int{}}
	c.walk(doc)
	return c.finish()
}

type list struct {
	ordered bool
	n       int
}

type conv struct {
	out     strings.Builder
	started bool // anything written yet
	nl      int  // trailing newlines in out
	pending int  // newlines required before the next text
	space   bool // a space is pending before the next word
	pre     int
	prefix  string // blockquote prefix applied at line starts
	lists   []*list

	links     []string
	linkIndex map[string]int
}

var skip = map[atom.Atom]bool{
	atom.Head: true, atom.Script: true, atom.Style: true, atom.Title: true,
	atom.Noscript: true, atom.Template: true, atom.Svg: true,
}

var lineBlocks = map[atom.Atom]bool{
	atom.Div: true, atom.Tr: true, atom.Dt: true, atom.Dd: true, atom.Section: true,
	atom.Article: true, atom.Header: true, atom.Footer: true, atom.Nav: true,
	atom.Aside: true, atom.Address: true, atom.Center: true, atom.Form: true,
	atom.Figure: true, atom.Caption: true, atom.Fieldset: true, atom.Main: true,
}

var paraBlocks = map[atom.Atom]bool{
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Table: true, atom.Dl: true,
}

func (c *conv) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		c.text(n.Data)
		return
	case html.ElementNode:
		if skip[n.DataAtom] {
			return
		}
	case html.DocumentNode:
	default:
		return
	}

	switch a := n.DataAtom; {
	case a == atom.Br:
		c.lineBreak()
		return
	case a == atom.Hr:
		c.block(1)
		c.word("────────────────────")
		c.block(1)
		return
	case a == atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			c.word("[" + alt + "]")
		}
		return
	case a == atom.P:
		if isEmpty(n) {
			// Outlook's <p>&nbsp;</p> is a deliberate blank line.
			c.block(1)
			c.blankLine()
			return
		}
		gap := 2
		if strings.Contains(attr(n, "class"), "MsoNormal") {
			gap = 1 // Outlook writes every line as its own zero-margin paragraph.
		}
		c.block(gap)
		c.children(n)
		c.block(gap)
		return
	case a == atom.Ul || a == atom.Ol:
		gap := 1 // nested lists hug their parent item
		if len(c.lists) == 0 {
			gap = 2
		}
		c.block(gap)
		c.lists = append(c.lists, &list{ordered: a == atom.Ol})
		c.children(n)
		c.lists = c.lists[:len(c.lists)-1]
		c.block(gap)
		return
	case a == atom.Li:
		c.block(1)
		marker := "•"
		if l := c.currentList(); l != nil && l.ordered {
			l.n++
			marker = fmt.Sprintf("%d.", l.n)
		}
		indent := strings.Repeat("  ", max(len(c.lists)-1, 0))
		c.word(indent + marker)
		c.space = true
		c.children(n)
		c.block(1)
		return
	case a == atom.Blockquote:
		c.block(2)
		saved := c.prefix
		c.prefix += "> "
		c.children(n)
		c.block(2)
		c.prefix = saved
		return
	case a == atom.Pre:
		c.block(2)
		c.pre++
		c.children(n)
		c.pre--
		c.block(2)
		return
	case a == atom.Td || a == atom.Th:
		c.space = true
		c.children(n)
		c.space = true
		return
	case a == atom.A:
		c.children(n)
		c.footnote(n)
		return
	case lineBlocks[a]:
		c.block(1)
		c.children(n)
		c.block(1)
		return
	case paraBlocks[a]:
		c.block(2)
		c.children(n)
		c.block(2)
		return
	}
	c.children(n)
}

func (c *conv) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

func (c *conv) currentList() *list {
	if len(c.lists) == 0 {
		return nil
	}
	return c.lists[len(c.lists)-1]
}

// block requests n line breaks before the next text: 1 ends the line, 2
// leaves a blank line.
func (c *conv) block(n int) {
	c.pending = max(c.pending, n)
	c.space = false
}

func (c *conv) flush() {
	if !c.started {
		c.pending = 0
		return
	}
	for c.nl < c.pending {
		c.out.WriteByte('\n')
		c.nl++
	}
	c.pending = 0
}

func (c *conv) lineBreak() {
	if !c.started {
		return
	}
	c.flush()
	c.out.WriteByte('\n')
	c.nl++
	c.space = false
}

func (c *conv) blankLine() {
	if !c.started {
		return
	}
	c.flush()
	for c.nl < 2 {
		c.out.WriteByte('\n')
		c.nl++
	}
}

func (c *conv) word(w string) {
	c.flush()
	if c.nl > 0 || !c.started {
		c.out.WriteString(c.prefix)
	} else if c.space {
		c.out.WriteByte(' ')
	}
	c.out.WriteString(w)
	c.space = false
	c.nl = 0
	c.started = true
}

func (c *conv) text(s string) {
	if c.pre > 0 {
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		for i, line := range lines {
			if i > 0 {
				c.lineBreak()
			}
			if line != "" {
				c.flush()
				if c.nl > 0 || !c.started {
					c.out.WriteString(c.prefix)
				}
				c.out.WriteString(line)
				c.nl = 0
				c.started = true
			}
		}
		return
	}
	if s == "" {
		return
	}
	if startsWithSpace(s) {
		c.space = true
	}
	words := strings.Fields(s)
	for i, w := range words {
		if i > 0 {
			c.space = true
		}
		c.word(w)
	}
	if len(words) > 0 && endsWithSpace(s) {
		c.space = true
	}
}

func (c *conv) footnote(n *html.Node) {
	href := strings.TrimSpace(attr(n, "href"))
	lower := strings.ToLower(href)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return
	}
	if strings.TrimSpace(textContent(n)) == href {
		return // the link text already shows the URL
	}
	idx, ok := c.linkIndex[href]
	if !ok {
		c.links = append(c.links, href)
		idx = len(c.links)
		c.linkIndex[href] = idx
	}
	c.space = false
	if c.started && c.nl == 0 {
		c.out.WriteString(fmt.Sprintf("[%d]", idx))
	} else {
		c.word(fmt.Sprintf("[%d]", idx))
	}
}

var manyBlankLines = regexp.MustCompile(`\n{3,}`)

func (c *conv) finish() string {
	lines := strings.Split(c.out.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	text := strings.TrimSpace(manyBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	if len(c.links) > 0 {
		var b strings.Builder
		b.WriteString(text)
		b.WriteString("\n\nLinks:\n")
		for i, l := range c.links {
			fmt.Fprintf(&b, "[%d] %s\n", i+1, l)
		}
		text = strings.TrimRight(b.String(), "\n")
	}
	return text
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return b.String()
}

// isEmpty reports whether an element has only whitespace (incl. &nbsp;) and no images.
func isEmpty(n *html.Node) bool {
	empty := true
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			empty = false
		}
		if n.Type == html.TextNode && strings.TrimFunc(n.Data, unicode.IsSpace) != "" {
			empty = false
		}
		for ch := n.FirstChild; ch != nil && empty; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return empty
}

func startsWithSpace(s string) bool {
	for _, r := range s {
		return unicode.IsSpace(r)
	}
	return false
}

func endsWithSpace(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsSpace(r[len(r)-1])
}
