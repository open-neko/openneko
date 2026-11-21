package webfront

import (
	"regexp"
	"strings"
)

// An answer that says nothing exists must rest on a query result that shows
// it: an empty list or a zero. A run whose results show neither gets one
// more turn to check.
var (
	answerFence  = regexp.MustCompile("(?s)```.*?```")
	nothingClaim = regexp.MustCompile(`(?i)\b(there (is|are|was|were) (currently )?no|no (\w+ ){0,2}(data|records?|rows|inventory|stock|sales|orders?|buyers|customers|results?|activity|revenue|units|transactions)|nothing (was )?(found|recorded))\b`)
)

func claimsNothing(answer string) bool {
	return nothingClaim.MatchString(answerFence.ReplaceAllString(answer, ""))
}

// showsNothing is true for data that holds an empty list, a zero or a null.
func showsNothing(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case float64:
		return v == 0
	case []any:
		if len(v) == 0 {
			return true
		}
		for _, item := range v {
			if showsNothing(item) {
				return true
			}
		}
	case map[string]any:
		if len(v) == 0 {
			return true
		}
		for _, item := range v {
			if showsNothing(item) {
				return true
			}
		}
	}
	return false
}

// resultShowsNothing reads an execute_graphql result as the tool events
// carry it.
func resultShowsNothing(result any) bool {
	data, ok := compileData(compileResultText(result))
	return ok && showsNothing(data)
}

func isGraphjinExecute(name string) bool { return strings.HasSuffix(name, "execute_graphql") }
