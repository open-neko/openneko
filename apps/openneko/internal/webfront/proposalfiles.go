package webfront

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
)

// shownFile is how the approval card shows one proposed file: as text in
// a named charset, or as a known binary type that no run executes.
type shownFile struct {
	Text    string
	Charset string
	Binary  string
}

var knownBinaries = map[string]struct {
	label string
	exts  []string
}{
	"image/png":          {"A PNG image", []string{".png"}},
	"image/jpeg":         {"A JPEG image", []string{".jpg", ".jpeg"}},
	"image/gif":          {"A GIF image", []string{".gif"}},
	"image/webp":         {"A WebP image", []string{".webp"}},
	"image/bmp":          {"A BMP image", []string{".bmp"}},
	"image/x-icon":       {"An icon", []string{".ico"}},
	"application/pdf":    {"A PDF document", []string{".pdf"}},
	"application/zip":    {"A ZIP archive", []string{".zip", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp"}},
	"application/x-gzip": {"A gzip archive", []string{".gz", ".tgz"}},
	"font/ttf":           {"A font", []string{".ttf"}},
	"font/otf":           {"A font", []string{".otf"}},
	"font/woff":          {"A font", []string{".woff"}},
	"font/woff2":         {"A font", []string{".woff2"}},
}

var declaredCharset = regexp.MustCompile(`(?i)(?:coding[:=]\s*|charset\s*=\s*["']?|encoding\s*=\s*["'])([-\w.:]+)`)

type unshownFile struct{ path string }

func (u unshownFile) Error() string {
	return fmt.Sprintf("OpenNeko cannot show the file %s on the approval card. A team skill holds text files, images, PDFs, fonts and archives.", u.path)
}

func showProposedFile(name string, data []byte) (shownFile, error) {
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		return shownFile{Text: string(data)}, nil
	}
	if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		if text, ok := decodeShowable(unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM), data); ok {
			return shownFile{Text: text, Charset: "UTF-16"}, nil
		}
		return shownFile{}, unshownFile{name}
	}
	if kind, ok := knownBinaries[http.DetectContentType(data)]; ok && slices.Contains(kind.exts, strings.ToLower(path.Ext(name))) {
		return shownFile{Binary: kind.label}, nil
	}
	if bytes.ContainsRune(data, 0) {
		return shownFile{}, unshownFile{name}
	}
	head := data[:min(len(data), 1024)]
	if m := declaredCharset.FindSubmatch(head); m != nil {
		if enc, err := htmlindex.Get(string(m[1])); err == nil {
			if text, ok := decodeShowable(enc, data); ok {
				name, _ := htmlindex.Name(enc)
				return shownFile{Text: text, Charset: name}, nil
			}
		}
	}
	if text, ok := decodeShowable(charmap.Windows1252, data); ok {
		return shownFile{Text: text, Charset: "windows-1252"}, nil
	}
	return shownFile{}, unshownFile{name}
}

// decodeShowable refuses a control character other than tab, line feed,
// form feed and carriage return, so nothing in the text stays unseen.
func decodeShowable(enc encoding.Encoding, data []byte) (string, bool) {
	out, err := enc.NewDecoder().Bytes(data)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	text := string(out)
	for _, r := range text {
		if r == utf8.RuneError || r < 0x20 && r != '\t' && r != '\n' && r != '\f' && r != '\r' || r >= 0x7F && r <= 0x9F {
			return "", false
		}
	}
	return text, true
}
