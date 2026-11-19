package webfront

import (
	"context"
	"regexp"
	"strings"
)

const (
	profileQuestionsHeading = "## Questions worth asking"
	businessSectionMaxRunes = 8000
)

var questionBullet = regexp.MustCompile(`^(?:[-*•]|\d+[.)])\s+`)

// splitProfileQuestions returns the profile without its questions section,
// and the questions of that section.
func splitProfileQuestions(profile string) (string, []string) {
	var body, questions []string
	inQuestions := false
	for _, line := range strings.Split(profile, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "## ") {
			if inQuestions = strings.EqualFold(t, profileQuestionsHeading); inQuestions {
				continue
			}
		}
		if !inQuestions {
			body = append(body, line)
		} else if q := strings.TrimSpace(questionBullet.ReplaceAllString(t, "")); q != "" {
			questions = append(questions, q)
		}
	}
	return strings.TrimSpace(strings.Join(body, "\n")), questions
}

// businessSection goes before the per-turn sections of the work prompt, so
// it stays in the part that providers cache.
func businessSection(profile string) string {
	body, _ := splitProfileQuestions(profile)
	if body == "" {
		return ""
	}
	if r := []rune(body); len(r) > businessSectionMaxRunes {
		body = strings.TrimSpace(string(r[:businessSectionMaxRunes]))
	}
	return "<business>\nThis is the business you work for, profiled from its own data. Use its vocabulary and its scale as context. Run a query for the current value before you state a number.\n\n" +
		body + "\n</business>"
}

func (a *API) currentBusinessProfile(ctx context.Context, orgID string) (string, error) {
	var profile *string
	err := noRows(a.DB.QueryRow(ctx, `select business_profile from customer_profile where org_id = $1 and is_current limit 1`, orgID).Scan(&profile))
	return deref(profile), err
}
