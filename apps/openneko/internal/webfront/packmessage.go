package webfront

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// guardPackBody checks a pack action's body before it is proposed and
// again before it runs: nothing in it may make the mail client, the chat
// service or the browser of the receiver fetch a URL, and each link shows
// its real host. HTML mail in a field (Resend html) is reduced to safe
// HTML; a raw MIME message (Gmail raw) that is not safe is refused.
func guardPackBody(kind string, body any) (any, error) {
	obj, _ := body.(*jsObj)
	if obj == nil {
		return body, nil
	}
	refuse := func(why string) error {
		return fmt.Errorf("pack action %s: %s", kind, why)
	}
	switch {
	case strings.HasPrefix(kind, "slack."):
		if found := slackFetchedField(obj); found != "" {
			return nil, refuse("the message holds " + found + ", which Slack fetches. OpenNeko posts no image from a URL; put the link in the text")
		}
		if why := slackLinkProblem(obj); why != "" {
			return nil, refuse(why)
		}
		for _, k := range []string{"blocks", "attachments"} {
			if why := slackURLProblem(obj.vals[k], "", ""); why != "" {
				return nil, refuse(why)
			}
		}
		if _, hasChannel := obj.vals["channel"]; hasChannel {
			obj.set("unfurl_links", false)
			obj.set("unfurl_media", false)
		}
	case kind == "parallel_search.web_search":
		if objective, ok := obj.vals["objective"].(string); ok && utf8.RuneCountInString(objective) > searchObjectiveMax {
			return nil, refuse(fmt.Sprintf("the search objective has %d characters; a search needs at most %d", utf8.RuneCountInString(objective), searchObjectiveMax))
		}
	case kind == "resend.email":
		if _, has := obj.vals["attachments"]; has {
			return nil, refuse("the email holds attachments. Resend fetches an attachment from its path; OpenNeko sends no attachment")
		}
		if h, ok := obj.vals["html"].(string); ok {
			safe, err := safeEmailHTML(h)
			if err != nil {
				return nil, refuse(err.Error())
			}
			obj.set("html", safe)
		}
	}
	if why := rawMIMEProblem(obj); why != "" {
		return nil, refuse(why)
	}
	if why := htmlFetchInStrings(obj); why != "" {
		return nil, refuse(why)
	}
	return obj, nil
}

const searchObjectiveMax = 500

var slackFetchKeys = map[string]bool{"image_url": true, "thumb_url": true, "thumbnail_url": true, "video_url": true, "author_icon": true,
	"footer_icon": true, "icon_url": true, "provider_icon_url": true}

var urlShaped = regexp.MustCompile(`(?i)^\s*(?:[a-z][a-z0-9+.-]*://|//|data:|www\.)`)

// slackURLProblem refuses every URL in blocks and attachments except the
// url of a button, which the approval card shows with its host, and of a
// link element whose text, if it has one, names its host.
func slackURLProblem(v any, key, parentType string) string {
	switch x := v.(type) {
	case string:
		lower, value := strings.ToLower(key), strings.TrimSpace(x)
		wholeURL := urlShaped.MatchString(value) && !strings.ContainsAny(value, " \t\r\n")
		if value == "" || !wholeURL && !strings.Contains(lower, "url") && !strings.Contains(lower, "link") && !slackFetchKeys[lower] {
			return ""
		}
		if key == "url" && (parentType == "button" || parentType == "link") {
			return ""
		}
		return fmt.Sprintf("the message holds the URL %s %q in its blocks or attachments. Put the link in the text", key, x)
	case *jsObj:
		t, _ := x.vals["type"].(string)
		if t == "button" || t == "link" {
			if u, ok := x.vals["url"].(string); ok {
				host, valid := linkHost(u)
				if !valid {
					return fmt.Sprintf("the %s link %q is not an http, https or mailto link", t, u)
				}
				if shown := slackShownText(x.vals["text"]); t == "link" && shown != "" && !showsHost(shown, host) {
					return fmt.Sprintf("the %s text %q hides its host %s; name the host in the text", t, shown, host)
				}
			}
		}
		for _, k := range x.ordered() {
			if why := slackURLProblem(x.vals[k], k, t); why != "" {
				return why
			}
		}
	case []any:
		for _, item := range x {
			if why := slackURLProblem(item, key, parentType); why != "" {
				return why
			}
		}
	}
	return ""
}

func slackShownText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case *jsObj:
		s, _ := x.vals["text"].(string)
		return s
	}
	return ""
}

func slackFetchedField(v any) string {
	switch x := v.(type) {
	case *jsObj:
		if t, _ := x.vals["type"].(string); t == "image" {
			return "an image block"
		}
		for _, k := range x.ordered() {
			if s, ok := x.vals[k].(string); ok && slackFetchKeys[k] && strings.TrimSpace(s) != "" {
				return fmt.Sprintf("%s %q", k, s)
			}
			if found := slackFetchedField(x.vals[k]); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range x {
			if found := slackFetchedField(item); found != "" {
				return found
			}
		}
	}
	return ""
}

var slackLink = regexp.MustCompile(`<((?:https?|mailto):[^|>]+)\|([^>]*)>`)

func slackLinkProblem(v any) string {
	switch x := v.(type) {
	case string:
		for _, m := range slackLink.FindAllStringSubmatch(x, -1) {
			if host, _ := linkHost(m[1]); !showsHost(m[2], host) {
				return fmt.Sprintf("the link text %q hides its host %s; name the host in the text", m[2], host)
			}
		}
	case *jsObj:
		for _, k := range x.ordered() {
			if why := slackLinkProblem(x.vals[k]); why != "" {
				return why
			}
		}
	case []any:
		for _, item := range x {
			if why := slackLinkProblem(item); why != "" {
				return why
			}
		}
	}
	return ""
}

var looksLikeHTML = regexp.MustCompile(`<[A-Za-z!/][^>]*>`)

func htmlFetchInStrings(v any) string {
	switch x := v.(type) {
	case string:
		if looksLikeHTML.MatchString(x) {
			return htmlProblem(x)
		}
	case *jsObj:
		for _, k := range x.ordered() {
			if found := htmlFetchInStrings(x.vals[k]); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range x {
			if found := htmlFetchInStrings(item); found != "" {
				return found
			}
		}
	}
	return ""
}

var (
	cssFetch    = regexp.MustCompile(`(?i)(?:url|image-set|image|cross-fade|element|src|paint|expression)\s*\(|@import|@font-face|behavior\s*:|-moz-binding`)
	cssComment  = regexp.MustCompile(`(?s)/\*.*?(?:\*/|$)`)
	cssEscape   = regexp.MustCompile(`\\(?:([0-9a-fA-F]{1,6})[ \t\r\n\f]?|(\n)|(.))`)
	fetchAttrs  = map[string]bool{"src": true, "srcset": true, "background": true, "poster": true, "data": true, "lowsrc": true, "dynsrc": true, "xlink:href": true}
	fetchTags   = map[atom.Atom]bool{atom.Img: true, atom.Image: true, atom.Picture: true, atom.Source: true, atom.Video: true, atom.Audio: true, atom.Iframe: true, atom.Frame: true, atom.Object: true, atom.Embed: true, atom.Link: true, atom.Script: true, atom.Track: true, atom.Svg: true, atom.Meta: true, atom.Base: true, atom.Form: true}
	allowedTags = map[atom.Atom]bool{atom.P: true, atom.Br: true, atom.Div: true, atom.Span: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
		atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true, atom.U: true, atom.Ul: true, atom.Ol: true, atom.Li: true, atom.A: true, atom.Table: true, atom.Thead: true,
		atom.Tbody: true, atom.Tfoot: true, atom.Tr: true, atom.Th: true, atom.Td: true, atom.Code: true, atom.Pre: true, atom.Blockquote: true, atom.Hr: true, atom.Small: true}
	cellAttrs = map[string]bool{"colspan": true, "rowspan": true}
)

// cssLoads checks the CSS as written, without comments, and with its
// escapes decoded, so "\75 rl(" and "u/**/rl(" count as url(.
func cssLoads(s string) bool {
	stripped := cssComment.ReplaceAllString(s, "")
	return cssFetch.MatchString(s) || cssFetch.MatchString(stripped) || cssFetch.MatchString(decodeCSSEscapes(stripped)) ||
		cssFetch.MatchString(decodeCSSEscapes(s))
}

func decodeCSSEscapes(s string) string {
	return cssEscape.ReplaceAllStringFunc(s, func(m string) string {
		sub := cssEscape.FindStringSubmatch(m)
		switch {
		case sub[1] != "":
			var r rune
			_, _ = fmt.Sscanf(sub[1], "%x", &r)
			if r == 0 || r > utf8.MaxRune {
				return "\uFFFD"
			}
			return string(r)
		case sub[2] != "":
			return ""
		}
		return sub[3]
	})
}

func parseFragment(s string) ([]*html.Node, error) {
	return html.ParseFragment(strings.NewReader(s), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func linkHost(href string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return strings.ToLower(u.Hostname()), u.Hostname() != ""
	case "mailto":
		return "", true
	}
	return "", false
}

func showsHost(text, host string) bool {
	return host == "" || strings.Contains(strings.ToLower(text), host)
}

// htmlProblem names the first thing in HTML that fetches a URL or a link
// whose text hides its host; "" means the HTML is safe as it is.
func htmlProblem(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "the HTML does not parse"
	}
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode {
			if fetchTags[n.DataAtom] {
				found = "the HTML holds a <" + n.Data + "> element, which loads content from a URL"
				return
			}
			if n.DataAtom == atom.Style && cssLoads(nodeText(n)) {
				found = "the HTML style loads a URL"
				return
			}
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if a.Namespace != "" {
					key = a.Namespace + ":" + key
				}
				if fetchAttrs[key] || key == "style" && cssLoads(a.Val) {
					found = "the HTML attribute " + key + " loads a URL"
					return
				}
			}
			if n.DataAtom == atom.A {
				href := attr(n, "href")
				host, ok := linkHost(href)
				if href != "" && !ok {
					found = fmt.Sprintf("the HTML link %q is not an http, https or mailto link", href)
					return
				}
				next := ""
				if n.NextSibling != nil && n.NextSibling.Type == html.TextNode {
					next = n.NextSibling.Data
				}
				if !showsHost(nodeText(n), host) && !strings.HasPrefix(next, " ("+host+")") {
					found = fmt.Sprintf("the HTML link text %q hides its host %s; name the host in the text", strings.TrimSpace(nodeText(n)), host)
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

// safeEmailHTML keeps text, the allowed tags, the cell spans and http,
// https and mailto links. It drops every other element with its content
// when the element loads or runs something, and keeps the text of the
// rest. A link whose text does not name its host gets the host after it.
func safeEmailHTML(s string) (string, error) {
	nodes, err := parseFragment(s)
	if err != nil {
		return "", fmt.Errorf("the email HTML does not parse")
	}
	var out bytes.Buffer
	var write func(*html.Node) error
	children := func(n *html.Node) error {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := write(c); err != nil {
				return err
			}
		}
		return nil
	}
	write = func(n *html.Node) error {
		switch n.Type {
		case html.TextNode:
			out.WriteString(html.EscapeString(n.Data))
			return nil
		case html.ElementNode:
		default:
			return children(n)
		}
		if fetchTags[n.DataAtom] || n.DataAtom == atom.Style || n.DataAtom == atom.Head || n.DataAtom == atom.Title || n.DataAtom == atom.Template || n.DataAtom == atom.Noscript {
			return nil
		}
		if !allowedTags[n.DataAtom] {
			return children(n)
		}
		el := &html.Node{Type: html.ElementNode, Data: n.Data, DataAtom: n.DataAtom}
		var host string
		keep := true
		for _, a := range n.Attr {
			key := strings.ToLower(a.Key)
			switch {
			case n.DataAtom == atom.A && key == "href":
				h, ok := linkHost(a.Val)
				if !ok {
					keep = false
					continue
				}
				host = h
				el.Attr = append(el.Attr, html.Attribute{Key: "href", Val: strings.TrimSpace(a.Val)})
			case cellAttrs[key]:
				el.Attr = append(el.Attr, html.Attribute{Key: key, Val: a.Val})
			}
		}
		if n.DataAtom == atom.A && !keep {
			return children(n)
		}
		if n.DataAtom == atom.Br || n.DataAtom == atom.Hr {
			return html.Render(&out, el)
		}
		out.WriteString("<" + el.Data)
		for _, a := range el.Attr {
			out.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
		}
		out.WriteString(">")
		if err := children(n); err != nil {
			return err
		}
		out.WriteString("</" + el.Data + ">")
		if n.DataAtom == atom.A && !showsHost(nodeText(n), host) {
			out.WriteString(" (" + html.EscapeString(host) + ")")
		}
		return nil
	}
	for _, n := range nodes {
		if err := write(n); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

// rawMIMEProblem checks every "raw" field, as Gmail takes a whole message
// in base64url: each HTML part must be safe as it is.
func rawMIMEProblem(v any) string {
	switch x := v.(type) {
	case *jsObj:
		for _, k := range x.ordered() {
			if s, ok := x.vals[k].(string); ok && k == "raw" {
				if why := mimeMessageProblem(s); why != "" {
					return why
				}
				continue
			}
			if why := rawMIMEProblem(x.vals[k]); why != "" {
				return why
			}
		}
	case []any:
		for _, item := range x {
			if why := rawMIMEProblem(item); why != "" {
				return why
			}
		}
	}
	return ""
}

var errRawNotBase64 = errors.New("the raw message is not base64")

func decodeRawMessage(raw string) (*mail.Message, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(raw), "="))
	if err != nil {
		if data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(raw)); err != nil {
			return nil, errRawNotBase64
		}
	}
	return mail.ReadMessage(bytes.NewReader(data))
}

func mimeMessageProblem(raw string) string {
	msg, err := decodeRawMessage(raw)
	if errors.Is(err, errRawNotBase64) {
		return err.Error()
	}
	if err != nil {
		return "the raw message does not parse as an email"
	}
	return mimePartProblem(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
}

func mimePartProblem(contentType, encoding string, body io.Reader, depth int) string {
	if depth > 8 {
		return "the raw message nests its parts too deep"
	}
	media, params, err := mime.ParseMediaType(contentType)
	if contentType == "" {
		media, err = "text/plain", nil
	}
	if err != nil {
		return "the raw message has a part with a bad Content-Type"
	}
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineStripper{r: body})
	}
	switch {
	case strings.HasPrefix(media, "multipart/"):
		r := multipart.NewReader(body, params["boundary"])
		for {
			part, err := r.NextRawPart()
			if err == io.EOF {
				return ""
			}
			if err != nil {
				return "the raw message has a part that does not parse"
			}
			if why := mimePartProblem(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1); why != "" {
				return why
			}
		}
	case media == "text/html":
		text, err := io.ReadAll(io.LimitReader(body, 4<<20))
		if err != nil {
			return "the raw message has an HTML part that does not decode"
		}
		return htmlProblem(string(text))
	case strings.HasPrefix(media, "text/"):
		return ""
	}
	return "the raw message holds a " + media + " part; OpenNeko sends no attachment"
}

type newlineStripper struct{ r io.Reader }

func (s *newlineStripper) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	out := p[:0]
	for _, b := range p[:n] {
		if b != '\r' && b != '\n' {
			out = append(out, b)
		}
	}
	return len(out), err
}
