package agent

import (
	"regexp"
	"strings"
	"sync"
)

// A bold span that fills a whole line starts a new executor note. A note
// leaves the harness whole, with its heading and its body.
const executorNotesPerRun = 100

var executorHeading = regexp.MustCompile(`^\s*\*\*([^*\n]+)\*\*\s*$`)

// executorNotes numbers one run's executor notes.
type executorNotes struct {
	mu    sync.Mutex
	count int
	emit  func(call int64, index int, text string)
}

func newExecutorNotes(emit func(call int64, index int, text string)) *executorNotes {
	return &executorNotes{emit: emit}
}

func (n *executorNotes) stream(call int64) *executorNoteStream {
	return &executorNoteStream{notes: n, call: call}
}

func (n *executorNotes) show(call int64, note string) {
	text := strings.TrimSpace(note)
	n.mu.Lock()
	if text == "" || n.count >= executorNotesPerRun {
		n.mu.Unlock()
		return
	}
	n.count++
	index := n.count
	n.mu.Unlock()
	n.emit(call, index, text)
}

// executorNoteStream reads one executor call and sends each note when the
// next heading or the end of the call closes it.
type executorNoteStream struct {
	notes *executorNotes
	call  int64
	line  strings.Builder
	note  strings.Builder
}

func (s *executorNoteStream) add(chunk string) {
	for chunk != "" {
		i := strings.IndexByte(chunk, '\n')
		if i < 0 {
			s.line.WriteString(chunk)
			return
		}
		s.line.WriteString(chunk[:i])
		s.endLine()
		chunk = chunk[i+1:]
	}
}

func (s *executorNoteStream) endLine() {
	line := s.line.String()
	s.line.Reset()
	if executorHeading.MatchString(line) {
		s.flush()
	}
	s.note.WriteString(line + "\n")
}

func (s *executorNoteStream) flush() {
	s.notes.show(s.call, s.note.String())
	s.note.Reset()
}

func (s *executorNoteStream) end() {
	if s.line.Len() > 0 {
		s.endLine()
	}
	s.flush()
}
