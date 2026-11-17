package agent

import (
	"regexp"
	"strings"
)

var (
	contractName = regexp.MustCompile("`?\\bneko_(cards|a2ui|value|vitals|followups)\\b`?")
	contractWord = map[string]string{
		"cards":     "charts and tables",
		"a2ui":      "charts and tables",
		"value":     "time saved",
		"vitals":    "key figures",
		"followups": "follow-up questions",
	}
	thoughtBoldLine = regexp.MustCompile(`^\s*\*\*([^*\n]+)\*\*\s*$`)
	thoughtMarkLine = regexp.MustCompile(`^\s*#{1,6}\s+(.+?)\s*$`)
	wordTail        = regexp.MustCompile("[\\w`]*$")
)

// plainContractNames puts plain words where model text names an answer
// fence of the output contract.
func plainContractNames(text string) string {
	return contractName.ReplaceAllStringFunc(text, func(name string) string {
		return contractWord[contractName.FindStringSubmatch(name)[1]]
	})
}

// thoughtFilter reads one stream of responder thinking. A heading line that
// names a program term is dropped; other lines get plain contract names. It
// holds back a line that may still become a heading and a word that may
// still become a contract name.
type thoughtFilter struct {
	pending string
	midLine bool
}

func (f *thoughtFilter) add(chunk string) string {
	f.pending += chunk
	var out strings.Builder
	for {
		i := strings.IndexByte(f.pending, '\n')
		if i < 0 {
			break
		}
		if text, keep := f.line(f.pending[:i]); keep {
			out.WriteString(text + "\n")
		}
		f.pending, f.midLine = f.pending[i+1:], false
	}
	if !f.midLine {
		start := strings.TrimLeft(f.pending, " \t")
		if start == "" || start[0] == '*' || start[0] == '#' {
			return out.String()
		}
	}
	cut := len(f.pending) - len(wordTail.FindString(f.pending))
	if cut > 0 {
		out.WriteString(plainContractNames(f.pending[:cut]))
		f.pending, f.midLine = f.pending[cut:], true
	}
	return out.String()
}

func (f *thoughtFilter) end() string {
	if f.pending == "" {
		return ""
	}
	out, _ := f.line(f.pending)
	f.pending, f.midLine = "", false
	return out
}

func (f *thoughtFilter) line(text string) (string, bool) {
	if !f.midLine {
		m := thoughtBoldLine.FindStringSubmatch(text)
		if m == nil {
			m = thoughtMarkLine.FindStringSubmatch(text)
		}
		if m != nil && executorNoteText(m[1]) == "" {
			return "", false
		}
	}
	return plainContractNames(text), true
}
