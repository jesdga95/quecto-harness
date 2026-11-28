package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TrashHome is the person's own trash, on the volume that holds their home
// folder. TrashTop is the top folder of the volume path is on, where another
// volume keeps its trash. Tests replace both so nothing reaches the real trash.
var (
	TrashHome = homeTrash
	TrashTop  = volumeTop
)

// errNoTrash is a folder on a volume with no trash this account can use.
var errNoTrash = errors.New("its volume has no trash this account can use, so it stays where it is")

// errNotTheFolder is an entry that no longer has the identity it was ordered
// moved with.
var errNotTheFolder = errors.New("the folder is not the one to remove")

// trashCan is a trash folder on one volume. files holds what is trashed. A
// freedesktop trash also has info, with a record for each item, whose path
// is relative to top on a volume other than the home one.
type trashCan struct {
	files, info, top string
	prepare          func(path, trash string) error
}

// moveToTrash renames the folder with identity id into its own volume's trash.
// The restore path is origin. The returned path names the folder in the trash.
func moveToTrash(path, id, origin string) (string, error) {
	return trashEntry(path, id, origin, true, nil)
}

func trashEntry(path, id, origin string, dir bool, prepare func(string, string) error) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !isEntry(info, id, dir) {
		return "", errNotTheFolder
	}
	can, err := trashOf(path, info)
	if err != nil {
		return "", err
	}
	can.prepare = prepare
	return can.put(path, id, origin, dir)
}

// isEntry reports whether info is a folder, or a regular file when dir is
// false, with identity id.
func isEntry(info os.FileInfo, id string, dir bool) bool {
	kind := info.Mode().IsRegular()
	if dir {
		kind = info.IsDir()
	}
	return kind && id != "" && FileID(info) == id
}

// put renames path into the trash under the first free name, the base of
// origin or that with a number after it. The identity is checked again right
// before the rename.
func (c trashCan) put(path, id, origin string, dir bool) (string, error) {
	base := filepath.Base(origin)
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name = base + " " + strconv.Itoa(i)
		}
		dest := filepath.Join(c.files, name)
		release, err := c.reserve(name, origin)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if err == nil && !isEntry(info, id, dir) {
			err = errNotTheFolder
		}
		if err == nil && c.prepare != nil {
			if err := c.prepare(path, dest); err != nil {
				release()
				return "", err
			}
		}
		if err == nil {
			beforeRename(path)
			err = renameNoReplace(path, dest)
		}
		if err == nil {
			if err := movedWhole(path, dest, id, dir, release); err != nil {
				return "", err
			}
			return dest, nil
		}
		release()
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free name for %s in %s", base, c.files)
}

// beforeRename runs between the last identity check of an entry and its
// rename. Tests replace it to put another entry under the name in between.
var beforeRename = func(path string) {}

// movedWhole checks that the entry renamed from path to dest has identity id.
// Another entry goes back under path and the rename counts as refused; an
// unreadable destination, or an entry that cannot go back, is an UncheckedMove.
func movedWhole(path, dest, id string, dir bool, release func()) error {
	info, err := lstatMoved(dest)
	if err != nil {
		return &UncheckedMove{Path: path, Trash: dest, Err: err}
	}
	if isEntry(info, id, dir) {
		return nil
	}
	if err := renameNoReplace(dest, path); err != nil {
		return &UncheckedMove{Path: path, Trash: dest, Foreign: true, observedID: FileID(info), Err: err}
	}
	release()
	return errNotTheFolder
}

// lstatMoved reads an entry just renamed into the trash. Tests replace it to
// stand for a trash whose contents cannot be read.
var lstatMoved = os.Lstat

// UncheckedMove is a rename into the trash whose result is not known to be
// the entry meant: Trash could not be read, or, when Foreign, holds another
// entry that could not go back to Path.
type UncheckedMove struct {
	Path, Trash string
	Foreign     bool
	observedID  string
	Err         error
}

func (e *UncheckedMove) Error() string {
	if e.Foreign {
		return fmt.Sprintf("another entry took the place of %s as it was moved, and is in the trash as %s (%v)", e.Path, e.Trash, e.Err)
	}
	return fmt.Sprintf("%s was moved to the trash as %s, which could not be checked (%v)", e.Path, e.Trash, e.Err)
}

func (e *UncheckedMove) Unwrap() error { return e.Err }

// reserve claims name in a freedesktop trash by creating its record, which
// fails when another item holds the name. release takes the record back.
func (c trashCan) reserve(name, path string) (release func(), err error) {
	if c.info == "" {
		return func() {}, nil
	}
	if _, err := os.Lstat(filepath.Join(c.files, name)); err == nil {
		return nil, fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	record := filepath.Join(c.info, name+".trashinfo")
	f, err := os.OpenFile(record, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	release = func() { os.Remove(record) }
	_, err = f.WriteString(c.trashInfo(path, time.Now()))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// trashInfo is the record a freedesktop trash keeps for path, deleted at.
// The path is absolute in the home trash and relative to top in another.
func (c trashCan) trashInfo(path string, at time.Time) string {
	if c.top != "" {
		if rel, err := filepath.Rel(c.top, path); err == nil {
			path = rel
		}
	}
	return "[Trash Info]\nPath=" + escapeTrashPath(path) + "\nDeletionDate=" + at.Format("2006-01-02T15:04:05") + "\n"
}

// escapeTrashPath percent-encodes every byte of p but the unreserved
// characters of a URI and the slash.
func escapeTrashPath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// xdgHomeTrash is the home trash of the freedesktop trash specification.
func xdgHomeTrash() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "Trash")
}

// xdgCan is the freedesktop trash at dir, made if its parent exists, when it
// and its two folders belong to this account and sit on device dev.
func xdgCan(dir string, dev uint64, top string) (trashCan, bool) {
	parent, err := os.Stat(filepath.Dir(dir))
	if err != nil || !parent.IsDir() {
		return trashCan{}, false
	}
	if d, ok := device(parent); !ok || d != dev {
		return trashCan{}, false
	}
	c := trashCan{files: filepath.Join(dir, "files"), info: filepath.Join(dir, "info"), top: top}
	for _, d := range []string{dir, c.files, c.info} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return trashCan{}, false
		}
		if !ownFolder(d, dev) {
			return trashCan{}, false
		}
	}
	return c, true
}

// xdgTopCan is the freedesktop trash of the volume whose top folder is top:
// .Trash/<uid> under a shared .Trash that is a real folder with the sticky
// bit, or else .Trash-<uid>.
func xdgTopCan(top string, dev uint64) (trashCan, bool) {
	uid := strconv.Itoa(os.Getuid())
	shared := filepath.Join(top, ".Trash")
	if info, err := os.Lstat(shared); err == nil && info.IsDir() && info.Mode()&fs.ModeSticky != 0 {
		if c, ok := xdgCan(filepath.Join(shared, uid), dev, top); ok {
			return c, true
		}
	}
	return xdgCan(filepath.Join(top, ".Trash-"+uid), dev, top)
}

// ownFolder reports whether dir is a folder, not a link, that this account
// owns privately, on device dev.
func ownFolder(dir string, dev uint64) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	d, ok := device(info)
	return ok && d == dev && ownedByMe(info)
}

// volumeTop is the highest folder above path on the same device: the top of
// its volume. Empty when the device is not known.
func volumeTop(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	dev, ok := device(info)
	if !ok {
		return ""
	}
	dir := filepath.Clean(path)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		pinfo, err := os.Lstat(parent)
		if err != nil {
			return ""
		}
		d, ok := device(pinfo)
		if !ok {
			return ""
		}
		if d != dev {
			return dir
		}
		dir = parent
	}
}
