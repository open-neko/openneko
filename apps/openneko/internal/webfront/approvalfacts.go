package webfront

import (
	"context"
	"fmt"
	"mime"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// approvalFacts is what an approval card shows first. OpenNeko takes it
// from the action kind and the payload, never from the model's text.
type approvalFacts struct {
	Headline string   `json:"headline"`
	Lines    []string `json:"lines"`
	Flags    []string `json:"flags"`
}

const (
	factLinesMax  = 10
	factValueMax  = 160
	factDepthMax  = 4
	factLongValue = 400
)

var actionLabels = map[string]string{
	"send_webhook":           "Send a webhook",
	"resend.email":           "Send an email",
	"google_workspace.gmail": "Send or draft an email",
	"slack.message":          "Post a Slack message",
	"user_admin":             "Change a user",
	"group_admin":            "Change a group",
	"channel_admin":          "Change a channel setting",
	"data_source_admin":      "Change a data source",
	"source_config_admin":    "Change the GraphJin config",
	"code_create_issue":      "File an issue",
	"code_draft_patch":       "Draft a code patch",
	graphjinAPIMutationKind:  "Change data through an API",
}

func actionLabel(kind string) string {
	if label, ok := actionLabels[kind]; ok {
		return label
	}
	words := strings.FieldsFunc(kind, func(r rune) bool { return r == '_' || r == '.' || r == ':' || r == '-' })
	if len(words) == 0 {
		return "Run an action"
	}
	text := strings.ToLower(strings.Join(words, " "))
	return strings.ToUpper(text[:1]) + text[1:]
}

type factLeaf struct {
	key   string
	value any
	depth int
}

func factLeaves(v any, key string, depth int, out *[]factLeaf) {
	switch x := v.(type) {
	case *jsObj:
		for _, k := range x.ordered() {
			factLeaves(x.vals[k], k, depth+1, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			factLeaves(x[k], k, depth+1, out)
		}
	case []any:
		for _, item := range x {
			factLeaves(item, key, depth, out)
		}
	case nil:
	default:
		*out = append(*out, factLeaf{key, x, depth})
	}
}

func factValue(v any) string {
	s := jsValueString(v)
	if n := utf8.RuneCountInString(s); n > factLongValue {
		return fmt.Sprintf("%d characters", n)
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > factValueMax {
		s = string([]rune(s)[:factValueMax]) + "…"
	}
	return s
}

var (
	addressParser   = mail.AddressParser{WordDecoder: &mime.WordDecoder{}}
	recipientFields = map[string]string{"to": "To", "cc": "Cc", "bcc": "Bcc", "reply_to": "Reply-To", "replyto": "Reply-To",
		"recipient": "To", "recipients": "To"}
)

// addressList reads one address or a comma list; nil when s is not a
// list of email addresses.
func addressList(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "@") {
		return nil
	}
	list, err := addressParser.ParseList(s)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, strings.ToLower(a.Address))
	}
	return out
}

func addressDomain(address string) string {
	_, domain, ok := strings.Cut(address, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(domain)
}

func urlHost(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

type factRecipient struct{ field, address string }

// rawRecipients reads the recipients and the subject from the message
// itself, as Gmail sends to the headers of raw and not to any other field.
func rawRecipients(raw string) ([]factRecipient, string, bool) {
	msg, err := decodeRawMessage(raw)
	if err != nil {
		return nil, "", false
	}
	var out []factRecipient
	for _, field := range []string{"To", "Cc", "Bcc", "Reply-To"} {
		value := msg.Header.Get(field)
		if strings.TrimSpace(value) == "" {
			continue
		}
		list, err := addressParser.ParseList(value)
		if err != nil {
			return nil, "", false
		}
		for _, a := range list {
			out = append(out, factRecipient{field, strings.ToLower(a.Address)})
		}
	}
	subject, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	return out, subject, true
}

// buildApprovalFacts needs the org's own email domains and the hosts that
// executed actions reached before, to name what is unusual.
func buildApprovalFacts(kind string, payload any, orgDomains []string, knownHost func(string) bool) approvalFacts {
	f := approvalFacts{Headline: actionLabel(kind), Lines: []string{}, Flags: []string{}}
	if kind == graphjinAPIMutationKind {
		operation, _ := jsGet(payload, "operation").(string)
		source, _ := jsGet(payload, "source").(string)
		f.Headline += ": " + factValue(operation+" on "+source)
	}
	var leaves []factLeaf
	factLeaves(payload, "", 0, &leaves)
	flag := func(s string) {
		if !slices.Contains(f.Flags, s) {
			f.Flags = append(f.Flags, s)
		}
	}
	var recipients []factRecipient
	var hosts, channels []string
	subject := ""
	shown := 0
	for _, leaf := range leaves {
		s, isText := leaf.value.(string)
		lower := strings.ToLower(leaf.key)
		switch {
		case isText && lower == "raw":
			found, subj, ok := rawRecipients(s)
			if !ok {
				flag("OpenNeko cannot read the recipients of this email")
				continue
			}
			recipients, subject = append(recipients, found...), subj
			continue
		case isText && recipientFields[lower] != "":
			for _, address := range addressList(s) {
				recipients = append(recipients, factRecipient{recipientFields[lower], address})
			}
		case isText && lower == "channel":
			channels = append(channels, strings.TrimSpace(s))
		}
		if leaf.depth <= factDepthMax && leaf.key != "" {
			shown++
			if len(f.Lines) < factLinesMax {
				f.Lines = append(f.Lines, leaf.key+": "+factValue(leaf.value))
			}
		}
		if !isText {
			continue
		}
		if recipientFields[lower] == "" {
			for _, address := range addressList(s) {
				if domain := addressDomain(address); domain != "" && !slices.Contains(orgDomains, domain) {
					flag("Goes to an outside domain: " + domain)
				}
			}
		}
		if host := urlHost(s); host != "" {
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
			flag("Reaches the host " + host)
			if knownHost != nil && !knownHost(host) {
				flag("No earlier action reached " + host)
			}
		}
		if (lower == "role" || lower == "roles" || lower == "newrole") && strings.EqualFold(strings.TrimSpace(s), "admin") {
			flag("Gives the admin role")
		}
	}
	var recipientLines, addresses []string
	for _, r := range recipients {
		domain := addressDomain(r.address)
		line := r.field + ": " + r.address
		if !slices.Contains(orgDomains, domain) {
			line += " (outside domain " + domain + ")"
			flag("Goes to an outside domain: " + domain)
		}
		recipientLines = append(recipientLines, line)
		if !slices.Contains(addresses, r.address) {
			addresses = append(addresses, r.address)
		}
	}
	if subject != "" {
		recipientLines = append(recipientLines, "Subject: "+factValue(subject))
	}
	f.Lines = append(recipientLines, f.Lines...)
	switch {
	case len(addresses) > 0:
		f.Headline += ": " + factValue(strings.Join(addresses, ", "))
	case len(hosts) > 0:
		f.Headline += ": " + strings.Join(hosts, ", ")
	case len(channels) > 0:
		f.Headline += ": " + factValue(strings.Join(channels, ", "))
	}
	if shown > factLinesMax {
		f.Lines = append(f.Lines, fmt.Sprintf("%d more fields in the details", shown-factLinesMax))
	}
	return f
}

func (a *API) orgEmailDomains(ctx context.Context, orgID string) []string {
	var domains []string
	rows, err := a.DB.Query(ctx, `select distinct lower(split_part(email, '@', 2)) from app_user where org_id = $1 and email like '%@%'`, orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}

func (a *API) knownActionHost(ctx context.Context, orgID string) func(string) bool {
	return func(host string) bool {
		var seen bool
		_ = a.DB.QueryRow(ctx, `select exists (select 1 from action_request where org_id = $1 and status = 'executed'
			and payload::text ~* $2)`, orgID, `://([^/@"\\]*@)?`+regexp.QuoteMeta(host)+`([:/?#"\\]|$)`).Scan(&seen)
		return seen
	}
}

func (a *API) approvalFactsFor(ctx context.Context, orgID, kind string, payload any) approvalFacts {
	return buildApprovalFacts(kind, payload, a.orgEmailDomains(ctx, orgID), a.knownActionHost(ctx, orgID))
}

func (a *API) withApprovalFacts(ctx context.Context, orgID string, list []actionRequestOut) {
	domains, known := a.orgEmailDomains(ctx, orgID), a.knownActionHost(ctx, orgID)
	for i := range list {
		payload, _ := jsParse(list[i].Payload)
		facts := buildApprovalFacts(list[i].Kind, payload, domains, known)
		list[i].Facts = &facts
	}
}
