package store

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// scratchName is the name of a folder made for a conversation without a
// project: the UTC date it was made and six random hex digits.
var scratchName = regexp.MustCompile(`^scratch-(\d{4}-\d{2}-\d{2})-[0-9a-f]{6}$`)

// scratchDate is the layout of the date in a scratch folder's name.
const scratchDate = "2006-01-02"

// MintScratch makes a new, empty folder for a conversation started without a
// project, private to the account, and returns its path with the links in
// root followed.
func MintScratch(root string, now time.Time) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("Create %s: check the permissions on its parent: %w", root, err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("Resolve %s: %w", root, err)
	}
	for range 5 {
		dir := filepath.Join(real, "scratch-"+now.UTC().Format(scratchDate)+"-"+randomHex(3))
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("Create %s: check the permissions on %s: %w", dir, real, err)
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s is not a folder", dir)
		}
		return dir, nil
	}
	return "", errors.New("Find a free name for a scratch folder: try again")
}

// IsScratch reports whether dir is one of the folders MintScratch makes in
// root, by name: the folder itself, not one inside it.
func IsScratch(root, dir string) bool {
	if root == "" || dir == "" {
		return false
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		real = filepath.Clean(root)
	}
	return filepath.Dir(filepath.Clean(dir)) == real && scratchName.MatchString(filepath.Base(dir))
}

// inRoot reports whether dir sits directly in root, by name or with the
// links in root followed.
func inRoot(root, dir string) bool {
	parent := filepath.Dir(dir)
	return parent == config.ResolveLinks(root) || parent == filepath.Clean(root)
}

// errNotTheFolder is a folder whose identity is not the one written down.
var errNotTheFolder = errors.New("the folder is not the one to remove")

// removalName is the private name a removal order's Aside gives a folder set
// aside by a removal that was cut short.
var removalName = regexp.MustCompile(`^\.removal-[0-9a-f]{12}$`)

var carryStageName = regexp.MustCompile(`^\.carry-[0-9a-f]{12}$`)

// randomHex is n random bytes in hex. rand.Read does not fail: it ends the
// program when the system has no randomness to give.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newest is the latest change time of anything under dir, links not followed.
// ok is false when part of the tree could not be read.
func newest(dir string) (time.Time, bool) {
	var latest time.Time
	ok := true
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return nil
		}
		info, err := d.Info()
		if err != nil {
			ok = false
			return nil
		}
		changed, known := changeTime(info)
		if !known {
			ok = false
			return fs.SkipAll
		}
		if changed.After(latest) {
			latest = changed
		}
		return nil
	})
	return latest, ok
}

// sameCopy checks that every file under src is in dst at the same path with
// the same bytes, and that src holds nothing but folders and files.
func sameCopy(src, dst string) error {
	from, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer from.Close()
	return checkCopy(from, dst)
}

func checkCopy(from *os.Root, dst string) error {
	if dst == "" {
		return errors.New("the removal order does not name where the files went")
	}
	to, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer to.Close()
	source, err := from.Stat(".")
	if err != nil {
		return err
	}
	target, err := to.Stat(".")
	if err != nil {
		return err
	}
	if os.SameFile(source, target) {
		return errors.New("the copy refers to the source folder")
	}
	return fs.WalkDir(from.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			info, err := to.Lstat(rel)
			if err != nil {
				return err
			}
			if !info.IsDir() || directoryAttrs(from, rel) {
				return fmt.Errorf("%s does not match its directory copy", rel)
			}
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a file the copy took", rel)
		}
		if !sameFile(from, to, rel) {
			return fmt.Errorf("%s does not match its copy in %s", rel, dst)
		}
		return nil
	})
}

// directoryAttrs reports metadata that creating the destination directory leaves behind.
func directoryAttrs(root *os.Root, rel string) bool {
	f, err := root.Open(rel)
	if err != nil {
		return true
	}
	defer f.Close()
	return hasAttrs(f)
}

// sameFile reports whether rel is a regular file in both roots with the same
// bytes, and carries no attribute in a that a copy leaves out. A file that
// cannot be read is not the same.
func sameFile(a, b *os.Root, rel string) bool {
	open := func(r *os.Root) (*os.File, int64, bool) {
		if info, err := r.Lstat(rel); err != nil || !info.Mode().IsRegular() {
			return nil, 0, false
		}
		f, err := r.Open(rel)
		if err != nil {
			return nil, 0, false
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, 0, false
		}
		return f, info.Size(), true
	}
	fa, na, ok := open(a)
	if !ok {
		return false
	}
	defer fa.Close()
	if hasAttrs(fa) {
		return false
	}
	fb, nb, ok := open(b)
	if !ok {
		return false
	}
	defer fb.Close()
	if na != nb {
		return false
	}
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		ka, erra := io.ReadFull(fa, ba)
		kb, errb := io.ReadFull(fb, bb)
		if ka != kb || !bytes.Equal(ba[:ka], bb[:kb]) {
			return false
		}
		if erra == io.EOF || erra == io.ErrUnexpectedEOF {
			return errb == erra
		}
		if erra != nil || errb != nil {
			return false
		}
	}
}

// Copy limits for a move out of a scratch folder.
const (
	carryEntries = 5000
	carryFile    = 1 << 30
	carryTotal   = 2 << 30
)

// dependencyDirs are folders a package manager fills, which a move leaves
// behind rather than copy when they sit at the top of the folder. Deeper down
// a folder of that name is the project's own and is copied.
var dependencyDirs = map[string]bool{"node_modules": true, ".venv": true, "vendor": true}

// repoNames are repository metadata, a folder or, in a linked worktree, a
// file, which a move never copies.
var repoNames = map[string]bool{".git": true, ".hg": true, ".svn": true}

// CarryScratch copies the files in src into dst at the same relative paths,
// keeping a path dst already has. Writes go through dst opened as a root, and
// each file appears whole under its final name or not at all.
func CarryScratch(src, dst string) (Carried, error) {
	from, err := os.OpenRoot(src)
	if err != nil {
		return Carried{}, err
	}
	defer from.Close()
	to, err := os.OpenRoot(dst)
	if err != nil {
		return Carried{}, err
	}
	defer to.Close()
	return CarryRoots(from, to, "")
}

// CarryRoots is CarryScratch between two folders already open. temps is a
// file outside both listing the names the copy makes in to before they are
// final, empty for none; a list left by a copy cut short is cleared first.
func CarryRoots(from, to *os.Root, temps string) (Carried, error) {
	var c Carried
	left, err := clearTemps(from, to, temps)
	if err != nil {
		return c, fmt.Errorf("remove what an earlier copy left: %w", err)
	}
	unproven := map[string]bool{}
	for _, name := range left {
		unproven[name] = true
	}
	copied, err := copiedEntries(temps)
	if err != nil {
		return c, err
	}
	log, err := openTemps(temps)
	if err != nil {
		return c, err
	}
	var total int64
	entries := 0
	err = fs.WalkDir(from.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if rel == "." {
			if err == nil && directoryAttrs(from, rel) {
				c.Attrs++
			}
			return err
		}
		if err != nil {
			c.Failed++
			return nil
		}
		if entries++; entries > carryEntries || total >= carryTotal {
			c.Stopped = true
			return fs.SkipAll
		}
		switch {
		case repoNames[d.Name()]:
			c.Held++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir() && dependencyDirs[d.Name()] && path.Dir(rel) == ".":
			c.Dependencies++
			return fs.SkipDir
		case d.IsDir():
			if directoryAttrs(from, rel) {
				c.Attrs++
			}
			perm := fs.FileMode(0o700)
			if info, err := d.Info(); err == nil {
				perm = info.Mode().Perm() | 0o700
			}
			if err := to.Mkdir(rel, perm); err != nil && !errors.Is(err, fs.ErrExist) {
				c.Failed++
				return fs.SkipDir
			}
			if info, err := to.Lstat(rel); err != nil || !info.IsDir() {
				// A file or a link stands where the folder would go.
				c.Failed++
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			c.Held++
			return nil
		case unproven[rel]:
			return nil
		}
		// A file an earlier run of this copy put in place, still with the
		// identity it had then, was copied by it.
		if e, ok := copied[rel]; ok {
			if size, ok := copiedSize(from, to, rel, e.ID); ok {
				c.Copied++
				if e.Attrs || directoryAttrs(from, rel) {
					c.Attrs++
				}
				total += size
				return nil
			}
		}
		n, err := copyOne(from, to, rel, carryTotal-total, log)
		switch {
		case errors.Is(err, fs.ErrExist):
			c.There++
		case errors.Is(err, errAttrs):
			c.Copied++
			c.Attrs++
			total += n
		case err != nil:
			c.Failed++
		default:
			c.Copied++
			total += n
		}
		return nil
	})
	if log.f != nil {
		log.f.Close()
		if err == nil {
			// The names each file used are gone by now unless a removal
			// failed, which keeps the list for the next copy.
			left, err = clearTemps(from, to, temps)
		}
	}
	left = append(left, log.kept...)
	slices.Sort(left)
	c.InterruptedNames = slices.Compact(left)
	c.Interrupted = len(c.InterruptedNames)
	return c, err
}

// ClearCarry removes what a copy cut short left in to and returns the names it
// kept, as CarryRoots does first. from is nil when the folder copied from is
// gone, and then a half-written file is kept.
func ClearCarry(from, to *os.Root, temps string) ([]string, error) {
	return clearTemps(from, to, temps)
}

// tempEntry is one line of a copy's list of names. A temporary file has its
// identity; a final name written in place, when links are not supported, is
// Partial until a later line records the completed copy.
type tempEntry struct {
	LinkName string `json:"link_name,omitempty"`
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Partial  bool   `json:"partial,omitempty"`
	Done     bool   `json:"done,omitempty"`
	Kept     bool   `json:"kept,omitempty"`
	// From is the name a file with identity ID had before cleanup moved it to
	// the private name Name to check it.
	From    string `json:"from,omitempty"`
	StageID string `json:"stage_id,omitempty"`
	// Copied is a final name the copy filled, kept on the list until the
	// result is saved, so a run again counts it as copied.
	Copied bool `json:"copied,omitempty"`
	Attrs  bool `json:"attrs,omitempty"`
}

// tempLog appends to the list of names a copy makes, each line on disk
// before the copy goes on.
type tempLog struct {
	f    *os.File
	kept []string
}

func openTemps(path string) (*tempLog, error) {
	if path == "" {
		return &tempLog{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return &tempLog{f: f}, nil
}

func (l *tempLog) add(e tempEntry) error {
	if l.f == nil {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(data, '\n')); err != nil {
		return err
	}
	return l.f.Sync()
}

// clearTemps removes the names a list at path records that are still in to
// with the identity recorded. Unresolved names stay on the list until the
// session records the copy result.
func clearTemps(from, to *os.Root, path string) ([]string, error) {
	return processTemps(from, to, path, true)
}

// ErrCarryDestination marks a read failure in the destination of an interrupted copy.
var ErrCarryDestination = errors.New("could not read the copy destination")

// ErrCarryRecord marks a copy's list of names that could not be read.
var ErrCarryRecord = errors.New("could not read the copy record")

// DamagedCarryRecord identifies a complete entry that cannot be used for recovery.
type DamagedCarryRecord struct {
	Path string
	Line int
}

func (e *DamagedCarryRecord) Error() string {
	return fmt.Sprintf("copy record %s is damaged at line %d", e.Path, e.Line)
}

func (e *DamagedCarryRecord) Unwrap() error { return ErrCarryRecord }

// InspectCarry lists unresolved names without changing the journal or its files.
// With the destination unavailable, every outstanding name is reported.
func InspectCarry(from, to *os.Root, path string) ([]string, error) {
	return processTemps(from, to, path, false)
}

func processTemps(from, to *os.Root, path string, clean bool) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	records, err := readCarryEntries(path)
	if err != nil || records == nil {
		return nil, err
	}
	var entries, copied []tempEntry
	finalNames := make(map[string]string)
	done := map[string]bool{}
	originals := map[string]tempEntry{}
	staged := map[string]tempEntry{}
	for _, e := range records {
		if e.Done {
			done[e.Name] = true
		}
		if e.Copied {
			e.Done = false
			copied = append(copied, e)
			finalNames[e.ID] = e.Name
		} else if e.From != "" && e.ID != "" {
			staged[e.Name] = e
		} else if !e.Done {
			entries = append(entries, e)
			if !e.Kept && e.ID != "" {
				originals[e.Name] = e
			}
		}
	}
	var kept []string
	moved := map[string]tempEntry{}
	for _, e := range staged {
		original := originals[e.From]
		final := ""
		if original.ID == e.ID {
			final = original.LinkName
			if original.Partial {
				final = original.Name
			}
		}
		name := e.Name
		if to != nil {
			name = recoverMoved(from, to, e, final, clean)
		}
		if name != "" {
			kept = append(kept, name)
			moved[name] = e
		}
	}
	for _, e := range entries {
		if (!e.Kept && e.ID == "") || (e.Partial && done[e.Name]) {
			continue
		}
		if to == nil {
			kept = append(kept, e.Name)
			continue
		}
		info, err := to.Lstat(e.Name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrCarryDestination, err)
		}
		if e.Kept || !info.Mode().IsRegular() || FileID(info) != e.ID {
			kept = append(kept, e.Name)
			continue
		}
		if e.Partial {
			match, err := compareCopyPrefix(from, to, e.Name, info)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrCarryDestination, err)
			}
			// A partial that differs from its source is still the copy's
			// own file, whichever side changed, so it is kept and named.
			if match != partialMatches {
				kept = append(kept, e.Name)
				continue
			}
		}
		final := e.LinkName
		if final == "" {
			final = finalNames[e.ID]
		}
		linked := !e.Partial && sameCopyLink(to, e.Name, final, info)
		if linked && clean && finalNames[e.ID] != final {
			// Record the completed link before removing its temporary name.
			entry := tempEntry{Name: final, ID: e.ID, Copied: true}
			records = append(records, entry)
			if err := writeCarryEntries(path, records); err != nil {
				return nil, err
			}
			copied = append(copied, entry)
			finalNames[e.ID] = final
			current, err := to.Lstat(e.Name)
			linked = err == nil && os.SameFile(current, info) && sameCopyLink(to, e.Name, final, current)
		}
		if !linked && carriesMetadata(to, e.Name, info) {
			kept = append(kept, e.Name)
			continue
		}
		if !clean {
			continue
		}
		// A second link goes; a sole one may hold an edit, so it is trashed.
		if linked {
			held := func(_ *os.Root, _ string, moved os.FileInfo) bool {
				return linkedTo(to, final, moved)
			}
			var saved tempEntry
			save := func(entry tempEntry) error {
				saved = entry
				records = append(records, entry)
				return writeCarryEntries(path, records)
			}
			if left, err := dropName(to, e.Name, info, held, false, save); left != "" {
				log.Printf("Kept %s, left by an interrupted copy: %v", left, err)
				kept = append(kept, left)
				if left == saved.Name || filepath.Dir(left) == saved.Name {
					moved[left] = saved
				}
			}
		} else if err := trashLeft(to, e.Name, e.ID, final, path); err != nil {
			log.Printf("Kept %s, left by an interrupted copy: %v", e.Name, err)
			kept = append(kept, e.Name)
		}
	}
	slices.Sort(kept)
	kept = slices.Compact(kept)
	if !clean {
		return kept, nil
	}
	if len(kept) > 0 || len(copied) > 0 {
		// A later retry needs the original identity and final name even when
		// another file temporarily prevents restoration.
		pending := map[string]bool{}
		for _, entry := range moved {
			if original := originals[entry.From]; original.ID == entry.ID && !pending[entry.From] {
				copied = append(copied, original)
				pending[entry.From] = true
			}
		}
		for _, name := range kept {
			if pending[name] {
				continue
			}
			entry, ok := moved[name]
			if !ok {
				entry = tempEntry{Name: name, Kept: true}
			}
			copied = append(copied, entry)
		}
		return kept, writeCarryEntries(path, copied)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return kept, nil
}

// recoverMoved reports the surviving private entry. Cleanup restores it without
// replacement or removes a verified duplicate when the volume cannot restore it.
func recoverMoved(from, to *os.Root, e tempEntry, final string, clean bool) string {
	info, err := to.Lstat(e.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil || !fs.ValidPath(e.From) || filepath.Dir(e.From) != filepath.Dir(e.Name) {
		return e.Name
	}
	parent, err := to.OpenRoot(filepath.Dir(e.Name))
	if err != nil {
		return e.Name
	}
	defer parent.Close()
	dir, err := parent.Open(".")
	if err != nil {
		return e.Name
	}
	defer dir.Close()
	base, private := filepath.Base(e.From), filepath.Base(e.Name)
	if e.StageID != "" && (!info.IsDir() || FileID(info) != e.StageID) {
		return e.Name
	}
	if !info.IsDir() {
		if clean && info.Mode().IsRegular() && FileID(info) == e.ID && dropRename(int(dir.Fd()), private, base) == nil {
			return ""
		}
		return e.Name
	}
	stage, err := parent.OpenRoot(private)
	if err != nil {
		return e.Name
	}
	defer stage.Close()
	inner, err := stage.Open(".")
	if err != nil {
		return e.Name
	}
	defer inner.Close()
	opened, err := inner.Stat()
	if err != nil || !os.SameFile(opened, info) {
		return e.Name
	}
	names, err := inner.Readdirnames(3)
	// A stop before StageID was recorded can leave an empty staging directory.
	if clean && errors.Is(err, io.EOF) && len(names) == 0 {
		if !carryStageName.MatchString(private) || hasAttrs(inner) {
			return e.Name
		}
		if removeEmptyAt(dir, private) == nil {
			return ""
		}
	}
	// macOS can store file metadata in a sibling named ._<filename>. Only
	// the payload is handled here. Any remaining sibling keeps its directory.
	if len(names) == 2 {
		if names[0] == "._"+names[1] {
			names[0], names[1] = names[1], names[0]
		}
		if names[1] == "._"+names[0] {
			names = names[:1]
		}
	}
	if err != nil || len(names) != 1 {
		return e.Name
	}
	child := names[0]
	left := filepath.Join(e.Name, child)
	current, err := stage.Lstat(child)
	if !clean || err != nil || !current.Mode().IsRegular() || FileID(current) != e.ID {
		return left
	}
	err = restoreLinked(parent, stage, inner, private, child, base, current)
	if err != nil {
		if !linkUnsupported(err) || !fs.ValidPath(final) || filepath.Dir(final) != filepath.Dir(e.From) {
			return left
		}
		held := func(r *os.Root, file string, info os.FileInfo) bool {
			return !carriesMetadata(r, file, info) && (heldIn(r, file, to, final) || from != nil && heldIn(r, file, from, final))
		}
		if removeHeld(stage, inner, child, current, held, true) != nil {
			return left
		}
	}
	if removeEmptyAt(dir, private) != nil {
		return e.Name
	}
	return ""
}

// restoreLinked publishes the file only if its former name is free. Its
// private link can then be removed while the published link still holds it.
func restoreLinked(parent, stage *os.Root, dir *os.File, private, child, base string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errNotHeld
	}
	if err := linkFile(parent, filepath.Join(private, child), base); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return removeHeld(stage, dir, child, info, func(_ *os.Root, _ string, moved os.FileInfo) bool {
		return linkedTo(parent, base, moved)
	}, false)
}

// trashLeft moves name, a file with identity id that a copy left in to, to
// the trash under the base of final, or of name when final is empty.
func trashLeft(to *os.Root, name, id, final, journal string) error {
	if final == "" {
		final = name
	}
	path := filepath.Join(to.Name(), name)
	trash, err := trashCopyFile(journal, path, id, filepath.Join(to.Name(), final))
	if err != nil {
		return err
	}
	log.Printf("Moved %s, left by an interrupted copy, to the trash as %s", path, trash)
	return nil
}

// unchanged reports whether now is the file before was, with the same size
// and modification and change times. A change time not known is a change.
func unchanged(before, now os.FileInfo) bool {
	if before == nil || now == nil || !os.SameFile(before, now) || before.Size() != now.Size() || !before.ModTime().Equal(now.ModTime()) {
		return false
	}
	a, okA := changeTime(before)
	b, okB := changeTime(now)
	return okA && okB && a.Equal(b)
}

func writeCarryEntries(path string, entries []tempEntry) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return config.WriteAtomic(path, buf.Bytes(), 0600)
}

// carriesMetadata reports whether the file name in r, with identity info,
// has an extended attribute, a resource fork or an access control list. A
// file that cannot be checked counts as having one.
func carriesMetadata(r *os.Root, name string, info os.FileInfo) bool {
	f, err := r.Open(name)
	if err != nil {
		return true
	}
	defer f.Close()
	now, err := f.Stat()
	if err != nil || !os.SameFile(now, info) {
		return true
	}
	return hasAttrs(f)
}

type partialMatch uint8

const (
	partialUnknown partialMatch = iota
	partialDifferent
	partialMatches
)

// A partial file may have been edited in place since the copy stopped.
// Only discard it if all its bytes can be recovered from the source.
func compareCopyPrefix(from, to *os.Root, name string, partial os.FileInfo) (partialMatch, error) {
	if from == nil || partial.Size() > carryFile {
		return partialUnknown, nil
	}
	prefix := func(root *os.Root, expected os.FileInfo) ([]byte, error) {
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, err
		}
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err = f.Stat()
		if err != nil || !info.Mode().IsRegular() || (expected != nil && !os.SameFile(info, expected)) {
			return nil, err
		}
		h := sha256.New()
		if _, err = io.CopyN(h, f, partial.Size()); err != nil {
			return nil, err
		}
		return h.Sum(nil), nil
	}
	dest, err := prefix(to, partial)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return partialUnknown, nil
	}
	if err != nil || dest == nil {
		return partialUnknown, err
	}
	original, err := from.Lstat(name)
	if err != nil {
		return partialUnknown, nil
	}
	source, err := prefix(from, original)
	if err != nil || source == nil {
		return partialUnknown, nil
	}
	partialHashed(name)
	now, err := to.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return partialUnknown, nil
	}
	if err != nil {
		return partialUnknown, err
	}
	if !unchanged(partial, now) {
		return partialUnknown, nil
	}
	if latest, err := from.Lstat(name); err != nil || !unchanged(original, latest) {
		return partialUnknown, nil
	}
	if !bytes.Equal(source, dest) {
		return partialDifferent, nil
	}
	return partialMatches, nil
}

// partialHashed runs between hashing a partial copy and checking it again.
// Tests replace it to change the file in between.
var partialHashed = func(name string) {}

// errTooLarge is a file past what is left of the copy limits.
var errTooLarge = errors.New("the file is past the copy limit")

// linkFile puts a second name on a file through a root. Tests replace it to
// stand for a volume without hard links.
var linkFile = func(r *os.Root, oldname, newname string) error { return r.Link(oldname, newname) }

// syncCopy flushes the temporary file before its final name is published.
var syncCopy = (*os.File).Sync

// linkUnsupported reports whether a link failed because the volume takes
// none, as exFAT, FAT and many network shares do.
func linkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EPERM)
}

// copyOne copies one file, at most room bytes, to a temporary name and links
// it into place, which fails rather than replace a file. Without links the
// final name is created new. Each name is listed before it is written.
func copyOne(from, to *os.Root, rel string, room int64, log *tempLog) (int64, error) {
	if _, err := to.Lstat(rel); err == nil {
		return 0, fs.ErrExist
	}
	in, err := from.Open(rel)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	limit := min(int64(carryFile), room)
	if info.Size() > limit {
		return 0, errTooLarge
	}
	var source io.Reader = in
	if info.Size() == 0 && info.Mode().IsRegular() {
		// Some files report zero length while providing bytes when read.
		probe := bufio.NewReader(in)
		if _, err := probe.Peek(1); err == io.EOF {
			return 0, copyEmpty(to, rel, in, info, log)
		} else if err != nil {
			return 0, err
		}
		source = probe
	}
	tmp := filepath.Join(filepath.Dir(rel), ".carry-"+randomHex(6))
	out, err := to.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	var linked string
	// copied is where the bytes of tmp are also held, when that is known.
	var copied func(*os.Root, string) bool
	defer func() {
		// Replay needs both names when the completed link could not be recorded.
		if linked == "" || err == nil {
			log.discard(to, tmp, out, rel, copied)
		} else {
			out.Close()
		}
	}()
	copied = func(parent *os.Root, private string) bool { return heldIn(parent, private, from, rel) }
	if err := logOpened(log, out, tempEntry{Name: tmp, LinkName: rel}); err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(source, limit+1))
	if err == nil && n > limit {
		err = errTooLarge
	}
	if err == nil {
		err = syncCopy(out)
	}
	if err != nil {
		return 0, err
	}
	// From here another writer may hold the file open, so only a final copy
	// of it proves its bytes are kept.
	copied = nil
	attrs := hasAttrs(in)
	err = linkFile(to, tmp, rel)
	if err != nil && linkUnsupported(err) {
		if _, err = out.Seek(0, io.SeekStart); err == nil {
			err = fillNew(to, rel, out, info.Mode().Perm(), log, attrs, tmp)
			// No link was made, so the temporary file has only its own name and
			// a writer holding it open keeps it from being unlinked.
			copied = func(parent *os.Root, private string) bool { return heldIn(parent, private, from, rel) }
			if err == nil {
				copied = func(parent *os.Root, private string) bool { return heldIn(parent, private, to, rel) }
			}
		}
	} else if err == nil {
		linked = rel
		err = logOpened(log, out, tempEntry{Name: rel, Copied: true, Attrs: attrs})
	}
	if err != nil {
		return 0, err
	}
	if attrs {
		return n, errAttrs
	}
	return n, nil
}

// An empty file can be published whole at its final name. Moving an empty
// temporary file on FAT or exFAT changes its identity.
func copyEmpty(to *os.Root, rel string, in *os.File, info os.FileInfo, log *tempLog) error {
	attrs := hasAttrs(in)
	if err := fillNew(to, rel, bytes.NewReader(nil), info.Mode().Perm(), log, attrs, ""); err != nil {
		return err
	}
	now, err := in.Stat()
	if err == nil && now.Size() != 0 {
		err = errors.New("the file changed while it was copied")
	}
	if err != nil {
		log.kept = append(log.kept, rel)
		return err
	}
	if attrs {
		return errAttrs
	}
	return nil
}

// errAttrs is a file copied without the extended attributes, resource fork or
// access control list it carries.
var errAttrs = errors.New("the file's extended attributes or access control list were not copied")

// copiedSize verifies the entire earlier copy before counting it on a retry.
func copiedSize(from, to *os.Root, rel, id string) (int64, bool) {
	dest, err := to.Lstat(rel)
	if err != nil || !dest.Mode().IsRegular() || FileID(dest) != id {
		return 0, false
	}
	source, err := from.Lstat(rel)
	if err != nil || !source.Mode().IsRegular() || source.Size() != dest.Size() {
		return 0, false
	}
	match, err := compareCopyPrefix(from, to, rel, dest)
	if err != nil || match != partialMatches {
		return 0, false
	}
	now, err := from.Lstat(rel)
	if err != nil || !os.SameFile(source, now) || source.Size() != now.Size() || !source.ModTime().Equal(now.ModTime()) {
		return 0, false
	}
	return dest.Size(), true
}

func readCarryEntries(path string) ([]tempEntry, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCarryRecord, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrCarryRecord, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCarryRecord, err)
	}
	entries := []tempEntry{}
	for line := range bytes.Lines(raw) {
		// An interrupted append may leave an incomplete JSON value at EOF.
		if line[len(line)-1] != '\n' && !json.Valid(line) {
			break
		}
		var e tempEntry
		if len(line) > 1<<20 || json.Unmarshal(line, &e) != nil || e.Name == "" {
			return nil, &DamagedCarryRecord{Path: path, Line: len(entries) + 1}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func copiedEntries(path string) (map[string]tempEntry, error) {
	entries, err := readCarryEntries(path)
	if err != nil {
		return nil, err
	}
	out := map[string]tempEntry{}
	for _, e := range entries {
		if e.Copied && e.ID != "" {
			out[e.Name] = e
		}
	}
	return out, nil
}

// logOpened records an entry with the identity of the open file.
func logOpened(log *tempLog, f *os.File, entry tempEntry) error {
	if log.f == nil {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	entry.ID = FileID(info)
	return log.add(entry)
}

// sameCopyLink verifies that the final directory entry still names the
// temporary file. A symlink or a single remaining link cannot preserve it.
func sameCopyLink(to *os.Root, name, final string, info os.FileInfo) bool {
	if !fs.ValidPath(final) || strings.EqualFold(name, final) || filepath.Dir(name) != filepath.Dir(final) {
		return false
	}
	return linkedTo(to, final, info)
}

func linkedTo(to *os.Root, final string, info os.FileInfo) bool {
	target, err := to.Lstat(final)
	return err == nil && hasOtherLinks(info) && os.SameFile(info, target)
}

// discard takes name, still the open file, out of the destination: unlinked
// through dropName when it is a second link of final or copied finds its bytes
// held elsewhere, else trashed, else kept when it holds metadata or cannot move.
func (l *tempLog) discard(to *os.Root, name string, file *os.File, final string, copied func(*os.Root, string) bool) {
	opened, statErr := file.Stat()
	attrs := hasAttrs(file)
	closeErr := file.Close()
	current, err := to.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil || closeErr != nil || statErr != nil || !os.SameFile(current, opened) {
		l.kept = append(l.kept, name)
		return
	}
	linked := sameCopyLink(to, name, final, current)
	attrs = !linked && attrs
	var held func(*os.Root, string, os.FileInfo) bool
	switch {
	case linked:
		held = func(_ *os.Root, _ string, moved os.FileInfo) bool {
			return linkedTo(to, final, moved)
		}
	case !attrs && copied != nil:
		held = func(parent *os.Root, private string, moved os.FileInfo) bool {
			return copied(parent, private) && !carriesMetadata(parent, private, moved)
		}
	}
	if held != nil {
		left, err := dropName(to, name, current, held, !linked, l.add)
		if left == "" {
			return
		}
		if linked && err != nil {
			log.Printf("Kept %s, left by a copy: %v", left, err)
			l.kept = append(l.kept, left)
			return
		}
		name = left
	}
	if !attrs && !carriesMetadata(to, name, current) {
		var journal string
		if l.f != nil {
			journal = l.f.Name()
		}
		err := trashLeft(to, name, FileID(current), final, journal)
		if err == nil {
			return
		}
		log.Printf("Kept %s, left by a copy that failed: %v", name, err)
	}
	l.kept = append(l.kept, name)
}

// errNotHeld is a file whose bytes are not known to be kept anywhere else.
var errNotHeld = errors.New("its bytes are not known to be held elsewhere")

// dropName records a private name before moving a file there to check it.
// An entry that fails the check is restored or kept under its recorded name.
func dropName(to *os.Root, name string, info os.FileInfo, held func(*os.Root, string, os.FileInfo) bool, idle bool, save func(tempEntry) error) (string, error) {
	parent, err := to.OpenRoot(filepath.Dir(name))
	if err != nil {
		return name, err
	}
	defer parent.Close()
	dir, err := parent.Open(".")
	if err != nil {
		return name, err
	}
	defer dir.Close()
	base := filepath.Base(name)
	tmp := ".carry-" + randomHex(6)
	private := filepath.Join(filepath.Dir(name), tmp)
	if err := save(tempEntry{Name: private, Kept: true, From: name, ID: FileID(info)}); err != nil {
		return name, err
	}
	beforeDrop(name, private)
	if err := dropRename(int(dir.Fd()), base, tmp); err != nil {
		if renameUnsupported(err) {
			return dropInDirectory(parent, dir, name, private, info, held, idle, save)
		}
		return name, err
	}
	err = removeHeld(parent, dir, tmp, info, held, idle)
	if err == nil {
		return "", nil
	}
	if dropRename(int(dir.Fd()), tmp, base) == nil {
		return name, err
	}
	return private, err
}

// dropRename moves a name to its private name and back without replacing
// either. Tests replace it to stand for a volume without that rename.
var dropRename = renameNoReplaceAt

// dropInDirectory is dropName for a volume without exclusive rename: the file
// moves into a new private directory and is checked there. One that fails
// returns through a new hard link when the volume supports it.
func dropInDirectory(parent *os.Root, dir *os.File, name, private string, info os.FileInfo, held func(*os.Root, string, os.FileInfo) bool, idle bool, save func(tempEntry) error) (left string, err error) {
	base := filepath.Base(name)
	current, err := parent.Lstat(base)
	if err != nil {
		return name, err
	}
	if !os.SameFile(current, info) || !held(parent, base, current) {
		return name, errNotHeld
	}
	tmp := filepath.Base(private)
	if err := parent.Mkdir(tmp, 0700); err != nil {
		return name, err
	}
	defer func() {
		if e := removeEmptyAt(dir, tmp); e != nil && left == "" {
			left, err = private, e
		}
	}()
	stage, err := parent.OpenRoot(tmp)
	if err != nil {
		return name, err
	}
	defer stage.Close()
	dest, err := stage.Open(".")
	if err != nil {
		return name, err
	}
	defer dest.Close()
	stageInfo, err := dest.Stat()
	if err != nil {
		return name, err
	}
	if err := save(tempEntry{Name: private, Kept: true, From: name, ID: FileID(info), StageID: FileID(stageInfo)}); err != nil {
		return name, err
	}
	child := "File-" + randomHex(6)
	beforeDrop(name, filepath.Join(private, child))
	if _, err := stage.Lstat(child); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = fs.ErrExist
		}
		return name, err
	}
	if err := renameInto(dir, base, dest, child); err != nil {
		return name, err
	}
	if err := removeHeld(stage, dest, child, info, held, idle); err != nil {
		moved, statErr := stage.Lstat(child)
		if statErr == nil && restoreLinked(parent, stage, dest, tmp, child, base, moved) == nil {
			return name, err
		}
		return filepath.Join(private, child), err
	}
	return "", nil
}

// removeHeld unlinks a staged file after verifying its identity and the
// other copy. A sole link can only be removed when no writer holds it.
func removeHeld(parent *os.Root, dir *os.File, name string, info os.FileInfo, held func(*os.Root, string, os.FileInfo) bool, idle bool) error {
	moved, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(info, moved) || !held(parent, name, moved) {
		return errNotHeld
	}
	now, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !unchanged(moved, now) {
		return errNotHeld
	}
	if idle {
		return removeIdle(dir, name, now)
	}
	return parent.Remove(name)
}

// renameUnsupported reports whether a rename failed because the volume or
// the system takes no rename that refuses to replace.
func renameUnsupported(err error) bool {
	for _, e := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.ENOSYS, errors.ErrUnsupported} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// beforeDrop runs after the cleanup name is recorded and before its rename.
var beforeDrop = func(name, private string) {}

// heldIn reports whether every byte of name in a is, in order, at the start of
// source in b, with neither file changed while they were read.
func heldIn(a *os.Root, name string, b *os.Root, source string) bool {
	before, err := a.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return false
	}
	srcBefore, err := b.Lstat(source)
	if err != nil || !srcBefore.Mode().IsRegular() || os.SameFile(before, srcBefore) || srcBefore.Size() < before.Size() {
		return false
	}
	hash := func(r *os.Root, rel string, want os.FileInfo) []byte {
		f, err := r.Open(rel)
		if err != nil {
			return nil
		}
		defer f.Close()
		if info, err := f.Stat(); err != nil || !os.SameFile(info, want) {
			return nil
		}
		h := sha256.New()
		if _, err := io.CopyN(h, f, before.Size()); err != nil {
			return nil
		}
		return h.Sum(nil)
	}
	mine, theirs := hash(a, name, before), hash(b, source, srcBefore)
	if mine == nil || theirs == nil || !bytes.Equal(mine, theirs) {
		return false
	}
	after, err := a.Lstat(name)
	if err != nil || !unchanged(before, after) {
		return false
	}
	srcAfter, err := b.Lstat(source)
	return err == nil && unchanged(srcBefore, srcAfter)
}

// fillNew creates rel, which must not exist, and copies src into it. source
// names the file in to that src reads, empty when there is none.
func fillNew(to *os.Root, rel string, src io.Reader, perm fs.FileMode, log *tempLog, attrs bool, source string) error {
	out, err := to.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	var copied func(*os.Root, string) bool
	if source != "" {
		copied = func(parent *os.Root, private string) bool { return heldIn(parent, private, to, source) }
	}
	if err := logOpened(log, out, tempEntry{Name: rel, Partial: true}); err != nil {
		log.discard(to, rel, out, rel, copied)
		return err
	}
	_, err = io.Copy(out, src)
	if err == nil {
		err = out.Sync()
	}
	if err == nil {
		err = logOpened(log, out, tempEntry{Name: rel, Done: true, Copied: true, Attrs: attrs})
	}
	if err != nil {
		// The name is visible, so it goes only when its bytes are a prefix of source.
		log.discard(to, rel, out, rel, copied)
		return err
	}
	if err := out.Close(); err != nil {
		log.kept = append(log.kept, rel)
		return err
	}
	return nil
}
