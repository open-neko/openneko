package agent

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Executor thinking sees raw tool rows and the prompt contract, so only a
// short heading of each note leaves the harness, never the note's body.
const (
	executorNoteMaxRunes = 80
	executorNotesPerRun  = 24
)

var (
	executorHeading   = regexp.MustCompile(`^\s*\*\*([^*\n]+)\*\*\s*$`)
	executorCodeMark  = regexp.MustCompile("[`_(){}\\[\\]<>=/\\\\$#@|~^*\"]|\\w\\.\\w|[a-z][A-Z]")
	executorInnerWord = regexp.MustCompile(`(?i)\b(executor|responder|action log|actions?|task definition|callables?|runtime|globalthis|harness|functions?|tools?|discovery|discover|prompt|javascript|js|mcp|variables?|json|schema|fields?|contract|final|conduct|sandbox|api|vitals|followups?|cards?|undefined|null|error|errors|debugging|debug|scope|keys?|instructions?|system|agent|worker|team|turn|dspy|signatures?|pars(e|es|ed|ing))\b`)
)

// executorNoteText returns the note as the user may see it, or "" when it
// names a program term or runs long.
func executorNoteText(heading string) string {
	text := strings.Join(strings.Fields(heading), " ")
	text = strings.TrimRight(text, " .:;,")
	if text == "" || utf8.RuneCountInString(text) > executorNoteMaxRunes {
		return ""
	}
	if executorCodeMark.MatchString(text) || executorInnerWord.MatchString(text) {
		return ""
	}
	return text
}

// executorNotes turns one run's executor thought chunks into short notes.
type executorNotes struct {
	mu    sync.Mutex
	shown map[string]bool
	count int
	emit  func(call int64, index int, text string)
}

func newExecutorNotes(emit func(call int64, index int, text string)) *executorNotes {
	return &executorNotes{shown: map[string]bool{}, emit: emit}
}

func (n *executorNotes) stream(call int64) *executorNoteStream {
	return &executorNoteStream{notes: n, call: call}
}

func (n *executorNotes) show(call int64, heading string) {
	text := executorNoteText(heading)
	key := strings.ToLower(text)
	n.mu.Lock()
	if text == "" || n.shown[key] || n.count >= executorNotesPerRun {
		n.mu.Unlock()
		return
	}
	n.shown[key] = true
	n.count++
	index := n.count
	n.mu.Unlock()
	n.emit(call, index, text)
}

// executorNoteStream reads one executor call. Only a bold span that fills a
// whole line is a heading: any other text may quote tool rows.
type executorNoteStream struct {
	notes *executorNotes
	call  int64
	line  strings.Builder
}

func (s *executorNoteStream) add(chunk string) {
	for chunk != "" {
		i := strings.IndexByte(chunk, '\n')
		if i < 0 {
			s.line.WriteString(chunk)
			return
		}
		s.line.WriteString(chunk[:i])
		s.end()
		chunk = chunk[i+1:]
	}
}

func (s *executorNoteStream) end() {
	if m := executorHeading.FindStringSubmatch(s.line.String()); m != nil {
		s.notes.show(s.call, m[1])
	}
	s.line.Reset()
}
