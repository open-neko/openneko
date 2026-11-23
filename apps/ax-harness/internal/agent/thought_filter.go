package agent

import "regexp"

var (
	contractName = regexp.MustCompile("`?\\bneko_(cards|a2ui|value|vitals|followups)\\b`?")
	contractWord = map[string]string{
		"cards":     "charts and tables",
		"a2ui":      "charts and tables",
		"value":     "time saved",
		"vitals":    "key figures",
		"followups": "follow-up questions",
	}
)

// plainContractNames puts plain words where model text names an answer
// fence of the output contract.
func plainContractNames(text string) string {
	return contractName.ReplaceAllStringFunc(text, func(name string) string {
		return contractWord[contractName.FindStringSubmatch(name)[1]]
	})
}
