package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type catalogView struct {
	Project string `json:"project,omitempty"`
	Tools   []Tool `json:"tools"`
}

type diskCatalog struct {
	Version int    `json:"version,omitempty"`
	Launch  string `json:"launch"`
	catalogView
}

const serverPart = `[A-Za-z0-9_-]{1,32}`

var catalogServer = regexp.MustCompile(`^` + serverPart + `$`)
var catalogName = regexp.MustCompile(`^(` + serverPart + `)(?:\.([0-9a-f]{64}))?\.json$`)
var catalogTemp = regexp.MustCompile(`^\.catalog-(?:([1-9][0-9]*)-)?[0-9]+$`)
var catalogDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Manager) statusPath(server string) string {
	return filepath.Join(m.cacheDir, ".last-"+server+".json")
}

func (m *Manager) readCatalog(path string) (diskCatalog, bool) {
	var c diskCatalog
	raw, err := m.readFile(path)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Tools == nil || c.Launch == "" || (c.Version != 0 && c.Version != 1) {
		return diskCatalog{}, false
	}
	return c, true
}

func (m *Manager) lastCatalog(s Server, projects []string) (catalogView, bool) {
	if m.cacheDir == "" || !catalogServer.MatchString(s.Name) {
		return catalogView{}, false
	}
	if !s.PerProject() {
		if tools, ok := m.readCache(s, keyFor(s, "")); ok {
			return catalogView{Tools: tools}, true
		}
	}
	if c, ok := m.readCatalog(m.statusPath(s.Name)); ok && c.Launch == m.launchDigest(s, keyFor(s, c.Project)) {
		return c.catalogView, true
	}
	if !s.PerProject() {
		return catalogView{}, false
	}
	// Older project catalogs stored the path only as a hash in their filename.
	// Match configured projects and use the newest listing for this launch.
	var latest catalogView
	var at time.Time
	for _, project := range append([]string{""}, projects...) {
		k := keyFor(s, project)
		info, err := os.Stat(m.catalogPath(k))
		if err != nil || !info.Mode().IsRegular() || (latest.Tools != nil && !info.ModTime().After(at)) {
			continue
		}
		if tools, ok := m.readCache(s, k); ok {
			latest, at = catalogView{Project: project, Tools: tools}, info.ModTime()
		}
	}
	return latest, latest.Tools != nil
}

func (m *Manager) evictCatalogs(server string) {
	entries, _ := os.ReadDir(m.cacheDir)
	for _, entry := range entries {
		match := catalogName.FindStringSubmatch(entry.Name())
		if match != nil && match[1] == server && entry.Type().IsRegular() {
			_ = os.Remove(filepath.Join(m.cacheDir, entry.Name()))
		}
	}
	if catalogServer.MatchString(server) {
		_ = os.Remove(m.statusPath(server))
	}
}

func (m *Manager) cleanCatalogs(servers map[string]Server) {
	entries, _ := os.ReadDir(m.cacheDir)
	projectServers := map[string]bool{}
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
		match := catalogName.FindStringSubmatch(catalog)
		if match == nil || (status && match[2] != "") {
			continue
		}
		s, configured := servers[match[1]]
		if configured {
			if match[2] != "" {
				projectServers[match[1]] = true
				continue
			}
			if status {
				continue
			}
		}
		c, ok := m.readCatalog(path)
		if !ok || !catalogDigest.MatchString(c.Launch) {
			continue
		}
		if configured {
			if c.Version != 0 {
				continue
			}
			digest := m.launchDigest(s, keyFor(s, ""))
			if digest == "" || digest == c.Launch {
				continue
			}
		}
		_ = os.Remove(path)
	}
	for name := range projectServers {
		m.trimProjects(name, "")
	}
}

const maxProjectCatalogs = 32

func (m *Manager) trimProjects(server, published string) {
	type catalog struct {
		name string
		at   time.Time
	}
	var files []catalog
	entries, _ := os.ReadDir(m.cacheDir)
	for _, entry := range entries {
		match := catalogName.FindStringSubmatch(entry.Name())
		if match == nil || match[1] != server || match[2] == "" || !entry.Type().IsRegular() {
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
