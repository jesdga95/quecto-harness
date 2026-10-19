package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// printCommands print the files named to them. A file one of them showed in a
// bash call counts as read, the way a read call counts it.
var printCommands = map[string]bool{"cat": true, "head": true, "tail": true, "sed": true, "nl": true, "bat": true}

// NoteShellReads records as read each project file that a printing command in
// a bash call was given. An argument that is not a file, such as a count or a
// sed script, is passed over.
func (r *Runner) NoteShellReads(command string) {
	if r.Seen == nil {
		return
	}
	for _, words := range shellSegments(command) {
		for len(words) > 0 && strings.Contains(words[0], "=") {
			words = words[1:]
		}
		if len(words) < 2 || !printCommands[filepath.Base(words[0])] {
			continue
		}
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "<>") {
				continue
			}
			path, err := r.resolve(w)
			if err != nil {
				continue
			}
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				r.Seen.note(path, info)
			}
		}
	}
}

// shellSegments splits a command into the words of each simple command in it,
// breaking at pipes, &&, || and semicolons and honouring quotes. It reads the
// common shapes of a command line, not the whole shell grammar.
func shellSegments(command string) [][]string {
	var segments [][]string
	var words []string
	var word strings.Builder
	inWord := false
	end := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	cut := func() {
		end()
		if len(words) > 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\'' || c == '"':
			inWord = true
			j := strings.IndexByte(command[i+1:], c)
			if j < 0 {
				word.WriteString(command[i+1:])
				i = len(command)
				break
			}
			word.WriteString(command[i+1 : i+1+j])
			i += j + 1
		case c == '\\' && i+1 < len(command):
			inWord = true
			word.WriteByte(command[i+1])
			i++
		case c == '|' || c == '&' || c == ';' || c == '\n' || c == '(' || c == ')':
			cut()
		case c == ' ' || c == '\t':
			end()
		default:
			inWord = true
			word.WriteByte(c)
		}
	}
	cut()
	return segments
}
