package webfront

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
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
}

func factLeaves(v any, key string, depth int, out *[]factLeaf) {
	switch x := v.(type) {
	case *jsObj:
		if depth >= factDepthMax {
			return
		}
		for _, k := range x.ordered() {
			factLeaves(x.vals[k], k, depth+1, out)
		}
	case map[string]any:
		if depth >= factDepthMax {
			return
		}
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
		*out = append(*out, factLeaf{key, x})
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

func addressDomain(s string) string {
	addr, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	_, domain, ok := strings.Cut(addr.Address, "@")
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

// buildApprovalFacts needs the org's own email domains and the hosts that
// executed actions reached before, to name what is unusual.
func buildApprovalFacts(kind string, target *string, payload any, orgDomains []string, knownHost func(string) bool) approvalFacts {
	f := approvalFacts{Headline: actionLabel(kind), Lines: []string{}, Flags: []string{}}
	if t := strings.TrimSpace(deref(target)); t != "" {
		f.Headline += ": " + factValue(t)
	}
	var leaves []factLeaf
	factLeaves(payload, "", 0, &leaves)
	flag := func(s string) {
		if !slices.Contains(f.Flags, s) {
			f.Flags = append(f.Flags, s)
		}
	}
	for _, leaf := range leaves {
		s, isText := leaf.value.(string)
		if len(f.Lines) < factLinesMax && leaf.key != "" {
			f.Lines = append(f.Lines, leaf.key+": "+factValue(leaf.value))
		}
		if !isText {
			continue
		}
		if domain := addressDomain(s); domain != "" && !slices.Contains(orgDomains, domain) {
			flag("Goes to an outside domain: " + domain)
		}
		if host := urlHost(s); host != "" {
			flag("Reaches the host " + host)
			if knownHost != nil && !knownHost(host) {
				flag("No earlier action reached " + host)
			}
		}
		lower := strings.ToLower(leaf.key)
		if (lower == "role" || lower == "roles" || lower == "newrole") && strings.EqualFold(strings.TrimSpace(s), "admin") {
			flag("Gives the admin role")
		}
	}
	if len(leaves) > factLinesMax {
		f.Lines = append(f.Lines, fmt.Sprintf("%d more fields in the details", len(leaves)-factLinesMax))
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
			and position($2 in payload::text) > 0)`, orgID, "://"+host).Scan(&seen)
		return seen
	}
}

func (a *API) approvalFactsFor(ctx context.Context, orgID, kind string, target *string, payload any) approvalFacts {
	return buildApprovalFacts(kind, target, payload, a.orgEmailDomains(ctx, orgID), a.knownActionHost(ctx, orgID))
}

func (a *API) withApprovalFacts(ctx context.Context, orgID string, list []actionRequestOut) {
	domains, known := a.orgEmailDomains(ctx, orgID), a.knownActionHost(ctx, orgID)
	for i := range list {
		payload, _ := jsParse(list[i].Payload)
		facts := buildApprovalFacts(list[i].Kind, list[i].Target, payload, domains, known)
		list[i].Facts = &facts
	}
}
