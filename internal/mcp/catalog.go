package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type catalogView struct {
	Project string `json:"project,omitempty"`
	Tools   []Tool `json:"tools"`
}

type diskCatalog struct {
	Version  int    `json:"version,omitempty"`
	Launch   string `json:"launch"`
	Template string `json:"template,omitempty"`
	catalogView
}

const serverPart = `[A-Za-z0-9_-]{1,32}`

var catalogServer = regexp.MustCompile(`^` + serverPart + `$`)
var catalogName = regexp.MustCompile(`^(` + serverPart + `|~[0-9a-f]{2,64})(?:\.([0-9a-f]{64}))?\.json$`)
var catalogTemp = regexp.MustCompile(`^\.catalog-(?:([1-9][0-9]*)-)?[0-9]+$`)
var catalogDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func catalogStem(server string) string {
	return "~" + hex.EncodeToString([]byte(server))
}

func catalogParts(name string) (server, project string, ok bool) {
	match := catalogName.FindStringSubmatch(name)
	if match == nil {
		return "", "", false
	}
	server = match[1]
	if raw, encoded := strings.CutPrefix(server, "~"); encoded {
		b, err := hex.DecodeString(raw)
		if err != nil || !catalogServer.Match(b) {
			return "", "", false
		}
		server = string(b)
	}
	return server, match[2], true
}

func (m *Manager) statusPath(server string) string {
	return filepath.Join(m.cacheDir, ".last-"+catalogStem(server)+".json")
}

func (m *Manager) readCatalog(path string) (diskCatalog, bool) {
	return decodeCatalog(m.readFile(path))
}

func decodeCatalog(raw []byte, err error) (diskCatalog, bool) {
	var c diskCatalog
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Tools == nil || c.Launch == "" || (c.Version != 0 && c.Version != 1) {
		return diskCatalog{}, false
	}
	return c, true
}

func (m *Manager) lastCatalog(s Server, projects []string) (catalogView, bool) {
	if m.cacheDir == "" || !catalogServer.MatchString(s.Name) {
		return catalogView{}, false
	}
	key := m.catalogKeyFor(false)
	if key == nil {
		return catalogView{}, false
	}
	if c, ok := m.readCatalog(m.statusPath(s.Name)); ok && c.Launch == digestFor(key, s, keyFor(s, c.Project)) {
		return c.catalogView, true
	}
	if !s.PerProject() {
		tools, ok := m.readCache(s, keyFor(s, ""))
		return catalogView{Tools: tools}, ok
	}
	// Some catalogs identify the project only by its path hash. Match
	// configured project paths and select the newest valid listing.
	var latest catalogView
	var at time.Time
	for _, project := range append([]string{""}, projects...) {
		k := keyFor(s, project)
		info, err := os.Stat(m.catalogPath(k))
		if err != nil || !info.Mode().IsRegular() || (latest.Tools != nil && !info.ModTime().After(at)) {
			continue
		}
		if c, ok := m.readCatalog(m.catalogPath(k)); ok && c.Launch == digestFor(key, s, k) {
			latest, at = catalogView{Project: project, Tools: c.Tools}, info.ModTime()
		}
	}
	if latest.Tools != nil {
		now := time.Now()
		_ = os.Chtimes(m.catalogPath(keyFor(s, latest.Project)), now, now)
	}
	return latest, latest.Tools != nil
}

var reportedCatalogPaths sync.Map

func warnUnrecognizedCatalog(path string) {
	if _, reported := reportedCatalogPaths.LoadOrStore(path, struct{}{}); !reported {
		log.Printf("Unrecognized catalog file %s: inspect it before removing it", path)
	}
}

func (m *Manager) evictCatalogs(server string) {
	entries, _ := os.ReadDir(m.cacheDir)
	for _, entry := range entries {
		name, status := strings.CutPrefix(entry.Name(), ".last-")
		owner, hash, ok := catalogParts(name)
		if !ok || owner != server || !entry.Type().IsRegular() || (status && hash != "") {
			continue
		}
		path := filepath.Join(m.cacheDir, entry.Name())
		c, valid := decodeCatalog(os.ReadFile(path))
		if !valid || !catalogDigest.MatchString(c.Launch) {
			warnUnrecognizedCatalog(path)
			continue
		}
		_ = os.Remove(path)
	}
}

func (m *Manager) cleanCatalogs(servers map[string]Server, alone bool) {
	entries, _ := os.ReadDir(m.cacheDir)
	projectServers := map[string]bool{}
	// Check migration targets before their sources and turn listings before probes.
	sort.SliceStable(entries, func(i, j int) bool {
		a, probeA := strings.CutPrefix(entries[i].Name(), ".last-")
		b, probeB := strings.CutPrefix(entries[j].Name(), ".last-")
		if probeA != probeB {
			return !probeA
		}
		return strings.HasPrefix(a, "~") && !strings.HasPrefix(b, "~")
	})
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(m.cacheDir, name)
		if tmp := catalogTemp.FindStringSubmatch(name); tmp != nil {
			if tmp[1] != "" {
				pid, err := strconv.Atoi(tmp[1])
				if err != nil || !catalogProcessGone(pid) {
					continue
				}
			}
			_ = os.Remove(path)
			continue
		}
		catalog, status := strings.CutPrefix(name, ".last-")
		server, projectHash, recognized := catalogParts(catalog)
		if !recognized || (status && projectHash != "") {
			continue
		}
		s, configured := servers[server]
		c, ok := m.readCatalog(path)
		if !ok || !catalogDigest.MatchString(c.Launch) {
			warnUnrecognizedCatalog(path)
			continue
		}
		key := m.catalogKeyFor(false)
		if key == nil {
			continue
		}
		if !strings.HasPrefix(catalog, "~") {
			var ambiguous bool
			s, configured, ambiguous = legacyServer(key, servers, server, c, projectHash)
			if ambiguous {
				log.Printf("Inspect %s to identify the server that owns it", path)
				continue
			}
			if configured {
				server = s.Name
			}
		}
		if !configured {
			if alone {
				_ = os.Remove(path)
			}
			continue
		}
		if projectHash != "" {
			projectServers[server] = true
		}
		digest := digestFor(key, s, keyFor(s, c.Project))
		if projectHash != "" && c.Version == 0 && c.Project == "" {
			if !s.PerProject() || (c.Template != "" && c.Template != digest) {
				if alone {
					_ = os.Remove(path)
				}
				continue
			}
			if c.Template == "" {
				if err := bindCatalogTemplate(path, c, digest); err != nil {
					log.Printf("Could not record the launch for %s: %v", path, err)
					continue
				}
			}
		} else if digest != c.Launch {
			if alone {
				_ = os.Remove(path)
			}
			continue
		}
		if !strings.HasPrefix(catalog, "~") {
			m.migrateCatalog(path, s, projectHash, status)
		}
	}
	for name := range projectServers {
		m.trimProjects(servers[name], "")
	}
}

// A project path hash cannot reconstruct the expanded command. Bind the
// catalog to the launch template until a known project can verify its digest.
func bindCatalogTemplate(path string, c diskCatalog, digest string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	c.Template = digest
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := stageFile(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Chtimes(tmp, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) migrateCatalog(path string, s Server, projectHash string, status bool) {
	name := catalogStem(s.Name)
	if projectHash != "" {
		name += "." + projectHash
	}
	target := filepath.Join(m.cacheDir, name+".json")
	if status {
		target = m.statusPath(s.Name)
		if !s.PerProject() {
			// A shared turn can be newer than the last separate Test listing.
			last, err := os.Stat(path)
			turn := m.cachePath(s.Name)
			info, statErr := os.Stat(turn)
			if err == nil && statErr == nil && info.ModTime().After(last.ModTime()) {
				c, ok := m.readCatalog(turn)
				if ok && c.Launch == digestFor(m.catalogKeyFor(false), s, keyFor(s, "")) {
					_ = os.Remove(path)
					return
				}
			}
		}
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if err := os.Rename(path, target); err != nil {
			log.Printf("Could not migrate catalog %s: %v", path, err)
		}
	} else if err == nil {
		prior, exists := m.readCatalog(target)
		source, ok := m.readCatalog(path)
		if exists && ok && prior.Launch == source.Launch {
			_ = os.Remove(path)
		} else {
			log.Printf("Inspect %s before replacing it with %s", target, path)
		}
	}
}

const maxProjectCatalogs = 32

func (m *Manager) trimProjects(s Server, published string) {
	type catalog struct {
		name string
		at   time.Time
	}
	var files []catalog
	entries, _ := os.ReadDir(m.cacheDir)
	key := m.catalogKeyFor(false)
	if key == nil {
		return
	}
	for _, entry := range entries {
		owner, project, ok := catalogParts(entry.Name())
		if !ok || owner != s.Name || project == "" || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(m.cacheDir, entry.Name())
		c, ok := m.readCatalog(path)
		if !ok || !catalogDigest.MatchString(c.Launch) {
			warnUnrecognizedCatalog(path)
			continue
		}
		if c.Project == "" || project != fmt.Sprintf("%x", sha256.Sum256([]byte(c.Project))) || c.Launch != digestFor(key, s, keyFor(s, c.Project)) {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, catalog{entry.Name(), info.ModTime()})
		}
	}
	if len(files) <= maxProjectCatalogs {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].name != files[j].name && (files[i].name == published || files[j].name == published) {
			return files[i].name == published
		}
		if files[i].at.Equal(files[j].at) {
			return files[i].name < files[j].name
		}
		return files[i].at.After(files[j].at)
	})
	for _, file := range files[maxProjectCatalogs:] {
		_ = os.Remove(filepath.Join(m.cacheDir, file.name))
	}
}

// Legacy filenames on case-insensitive volumes can have another server's casing.
func legacyServer(key []byte, servers map[string]Server, name string, c diskCatalog, hash string) (server Server, configured, ambiguous bool) {
	exact, ok := servers[name]
	launch := c.Launch
	if hash != "" && c.Version == 0 && c.Project == "" {
		launch = c.Template
	}
	var candidates []Server
	var variants int
	for n, s := range servers {
		if !strings.EqualFold(n, name) {
			continue
		}
		variants++
		if launch != "" && launch == digestFor(key, s, keyFor(s, c.Project)) {
			if n == name {
				return s, true, false
			}
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true, false
	}
	return exact, ok, len(candidates) > 1 || variants > 1 && (launch == "" || key == nil)
}

func writableCatalogDir(dir string) bool {
	if privateCatalogDir(dir) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, ".catalog-key"))
	return os.IsNotExist(err) || err == nil && privateKeyInfo(info)
}

// withCatalogWrite serializes disk publication, migration and pruning. Each
// configured manager holds a shared directory lock between writes. Only a
// sole manager removes catalogs on the strength of its configuration.
func (m *Manager) withCatalogWrite(work func(alone bool)) {
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil || !writableCatalogDir(m.cacheDir) {
		return
	}
	lock, err := os.OpenFile(filepath.Join(m.cacheDir, ".catalog-write.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		m.warnCatalogLock(err)
		return
	}
	defer lock.Close()
	if err := lockCatalogKey(lock); err != nil {
		m.warnCatalogLock(err)
		return
	}
	if !writableCatalogDir(m.cacheDir) {
		return
	}
	if m.lease != nil {
		info, e := m.lease.Stat()
		current, err := os.Lstat(m.cacheDir)
		if e != nil || err != nil || !os.SameFile(info, current) {
			m.lease.Close()
			m.lease = nil
		}
	}
	if m.lease == nil {
		m.lease, err = os.Open(m.cacheDir)
		if err != nil {
			return
		}
	}
	alone, err := catalogLease(m.lease)
	if err != nil {
		m.warnCatalogLock(err)
		return
	}
	defer shareCatalogLease(m.lease)
	work(alone)
}

func (m *Manager) warnCatalogLock(err error) {
	m.lockWarning.Do(func() {
		log.Printf("MCP disk cache unavailable: could not lock %s: %v. Use a filesystem that supports file locks", m.cacheDir, err)
	})
}

// Revalidate staged data under the write lock. A peer can own a listing
// at the same path for a different launch.
func (m *Manager) publishCatalog(tmp, path string, s Server, k instKey, key []byte, alone bool) bool {
	if tmp == "" || key == nil {
		return false
	}
	c, ok := decodeCatalog(os.ReadFile(tmp))
	if !ok || c.Launch != digestFor(key, s, k) {
		return false
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			log.Printf("Inspect %s before replacing it", path)
			return false
		}
		prior, ok := m.readCatalog(path)
		if !ok || !catalogDigest.MatchString(prior.Launch) {
			warnUnrecognizedCatalog(path)
			return false
		}
		previousKey := k
		if prior.Project != "" || path == m.statusPath(s.Name) {
			previousKey = keyFor(s, prior.Project)
		}
		if !alone && prior.Launch != digestFor(key, s, previousKey) {
			return false
		}
	} else if !os.IsNotExist(err) {
		return false
	}
	return os.Rename(tmp, path) == nil
}
