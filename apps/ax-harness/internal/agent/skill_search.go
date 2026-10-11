package agent

import (
	"sort"
	"strings"
	"unicode"

	ax "github.com/ax-llm/ax/packages/go"
)

const (
	skillMatchesPerSearch = 3
	maxSkillMatches       = 5
)

// searchSkills ranks staged skills for each discover() query by the words its
// name and description share with the query. An exact name always ranks first.
func searchSkills(skills []Skill, searches []ax.Value) []Skill {
	var found []Skill
	seen := map[string]bool{}
	for _, search := range searches {
		query := searchText(search)
		words := skillWords(query)
		if len(words) == 0 {
			continue
		}
		type scored struct {
			skill Skill
			score int
		}
		var ranked []scored
		for _, skill := range skills {
			score := 0
			if strings.EqualFold(skill.Name, strings.TrimSpace(query)) {
				score += 100
			}
			name, description := skillWords(skill.Name), skillWords(skill.Description)
			for word := range words {
				if name[word] {
					score += 3
				}
				if description[word] {
					score++
				}
			}
			if score > 0 {
				ranked = append(ranked, scored{skill, score})
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		for i := 0; i < len(ranked) && i < skillMatchesPerSearch; i++ {
			if name := ranked[i].skill.Name; !seen[name] && len(found) < maxSkillMatches {
				seen[name] = true
				found = append(found, ranked[i].skill)
			}
		}
	}
	return found
}

func searchText(search ax.Value) string {
	switch v := search.(type) {
	case string:
		return v
	case map[string]ax.Value:
		for _, key := range []string{"query", "search", "name", "id"} {
			if text, ok := v[key].(string); ok {
				return text
			}
		}
	}
	return ""
}

// skillWords splits text into lower-case words of three letters or more.
func skillWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(word) >= 3 {
			words[strings.TrimSuffix(word, "s")] = true
		}
	}
	return words
}
