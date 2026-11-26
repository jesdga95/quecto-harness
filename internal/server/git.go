package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/agent"
	"github.com/jesdga95/quecto-harness/internal/git"
)

// logLimit is how many commits the history lists.
const logLimit = 60

// repo resolves the project a git request names. A missing binary and a
// folder outside any repository are states the panel draws, so they answer
// 200 with a flag rather than an error.
func (s *Server) repo(w http.ResponseWriter, r *http.Request, project string) (*git.Repo, bool) {
	if !s.cfg.HasProject(project) {
		writeError(w, http.StatusNotFound, "No project at that path: add it first")
		return nil, false
	}
	if !s.projectOK(w, project) {
		return nil, false
	}
	repo, err := git.Open(r.Context(), project)
	switch {
	case errors.Is(err, git.ErrMissing):
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "repo": false})
		return nil, false
	case errors.Is(err, git.ErrNotRepo):
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "repo": false})
		return nil, false
	case err != nil:
		writeGitError(w, "Read the repository", err)
		return nil, false
	}
	if !s.repoOK(w, r, repo) {
		return nil, false
	}
	return repo, true
}

// repoOK refuses a repository whose working tree or git folders overlap the
// harness state folder or the scratch folder, even when the project is a part
// of it that does not: a stash, a commit of everything or a branch switch
// reaches the whole tree.
func (s *Server) repoOK(w http.ResponseWriter, r *http.Request, repo *git.Repo) bool {
	dirs, err := repo.Dirs(r.Context())
	if err != nil {
		writeGitError(w, "Read the repository", err)
		return false
	}
	for _, d := range dirs {
		if !s.checked(w, s.cfg.CheckRepo(d)) {
			return false
		}
	}
	return true
}

// writeGitError reports a failed git command with what git said, which is the
// only account of why.
func writeGitError(w http.ResponseWriter, what string, err error) {
	msg := err.Error()
	var ge *git.Error
	if errors.As(err, &ge) {
		msg = ge.Message
	}
	status := http.StatusConflict
	if errors.Is(err, git.ErrMissing) {
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, what+" failed: "+msg)
}

// getGit is the working tree for the panel.
func (s *Server) getGit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, ok := s.repo(w, r, q.Get("project"))
	if !ok {
		return
	}
	st, err := repo.Status(r.Context())
	if err != nil {
		writeGitError(w, "Read the status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "repo": true, "status": st,
	})
}

func (s *Server) getGitLog(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	commits, err := repo.Log(r.Context(), logLimit)
	if err != nil {
		writeGitError(w, "Read the history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

func (s *Server) getGitShow(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, ok := s.repo(w, r, q.Get("project"))
	if !ok {
		return
	}
	hash := q.Get("hash")
	if hash == "" || strings.HasPrefix(hash, "-") {
		writeError(w, http.StatusBadRequest, "Name a commit by its hash")
		return
	}
	files, err := repo.Show(r.Context(), hash)
	if err != nil {
		writeGitError(w, "Read the commit", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) getGitBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	local, remote, err := repo.Branches(r.Context())
	if err != nil {
		writeGitError(w, "List the branches", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"local": local, "remote": remote})
}

func (s *Server) getGitPatch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	patch, err := repo.Patch(r.Context(), false)
	if err != nil {
		writeGitError(w, "Build the diff", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patch": patch})
}

func (s *Server) postGitInit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.cfg.HasProject(body.Project) {
		writeError(w, http.StatusNotFound, "No project at that path: add it first")
		return
	}
	// A repository is made at the project, so the project is checked as one.
	if !s.checked(w, s.cfg.CheckRepo(body.Project)) {
		return
	}
	// A project already inside a repository is checked against all of it.
	switch repo, err := git.Open(r.Context(), body.Project); {
	case err == nil:
		if !s.repoOK(w, r, repo) {
			return
		}
	case errors.Is(err, git.ErrNotRepo), errors.Is(err, git.ErrMissing):
	default:
		writeGitError(w, "Read the repository", err)
		return
	}
	if err := git.Init(r.Context(), body.Project); err != nil {
		writeGitError(w, "Initialize the repository", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postGitAction runs one write named in the path. Each takes the project and
// whatever fields its own operation reads.
func (s *Server) postGitAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string   `json:"project"`
		Paths   []string `json:"paths"`
		// Hunk actions.
		Hunk   *git.HunkRef `json:"hunk"`
		Action string       `json:"action"`
		// Commit.
		Message string `json:"message"`
		Amend   bool   `json:"amend"`
		All     bool   `json:"all"`
		Push    bool   `json:"push"`
		// Checkout.
		Branch string `json:"branch"`
		Create bool   `json:"create"`
		Start  string `json:"start"`
		Stash  bool   `json:"stash"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	repo, ok := s.repo(w, r, body.Project)
	if !ok {
		return
	}
	ctx := r.Context()
	reply := map[string]any{"ok": true}
	var err error
	var what string
	switch op := r.PathValue("op"); op {
	case "stage":
		what, err = "Stage", repo.Stage(ctx, body.Paths)
	case "unstage":
		what, err = "Unstage", repo.Unstage(ctx, body.Paths)
	case "discard":
		what, err = "Discard", repo.Discard(ctx, body.Paths)
	case "hunk":
		if body.Hunk == nil {
			writeError(w, http.StatusBadRequest, "Name the hunk to change")
			return
		}
		what, err = "Change the hunk", repo.ApplyHunk(ctx, *body.Hunk, body.Action)
		if errors.Is(err, git.ErrHunkMoved) {
			writeError(w, http.StatusConflict, "That hunk has changed since it was shown: look again and retry")
			return
		}
	case "commit":
		what = "Commit"
		var hash string
		hash, err = repo.Commit(ctx, git.CommitRequest{Message: body.Message, Amend: body.Amend, All: body.All})
		if err == nil {
			reply["hash"] = hash
			if body.Push {
				if perr := repo.Push(ctx); perr != nil {
					// The commit stands; only the push is reported.
					var ge *git.Error
					msg := perr.Error()
					if errors.As(perr, &ge) {
						msg = ge.Message
					}
					reply["push_error"] = "Committed " + hash + ", but the push failed: " + msg
				}
			}
		}
	case "undo":
		what, err = "Undo the commit", repo.Undo(ctx)
	case "fetch":
		what, err = "Fetch", repo.Fetch(ctx)
	case "pull":
		what, err = "Pull", repo.Pull(ctx)
	case "push":
		what, err = "Push", repo.Push(ctx)
	case "stash":
		what, err = "Stash", repo.Stash(ctx, "")
	case "checkout":
		what, err = "Switch branch", repo.Checkout(ctx, git.CheckoutRequest{
			Branch: body.Branch, Create: body.Create, Start: body.Start, Stash: body.Stash,
		})
	default:
		writeError(w, http.StatusNotFound, "No git action named "+strconv.Quote(op))
		return
	}
	if err != nil {
		writeGitError(w, what, err)
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

// postGitDraft asks the session's model for a commit message for what would be
// committed: the staged changes, or every change when nothing is staged.
func (s *Server) postGitDraft(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if sess.Loose {
		writeError(w, http.StatusConflict, "No folder: this conversation has no repository")
		return
	}
	repo, ok := s.repo(w, r, sess.Project)
	if !ok {
		return
	}
	ctx := r.Context()
	patch, err := repo.Patch(ctx, repo.HasStaged(ctx))
	if err != nil {
		writeGitError(w, "Build the diff", err)
		return
	}
	if strings.TrimSpace(patch) == "" {
		writeError(w, http.StatusConflict, "There is nothing to commit")
		return
	}
	summary, body, err := s.engine.DraftCommit(ctx, sess.ID, patch, repo.Subjects(ctx, 10))
	switch {
	case errors.Is(err, agent.ErrNotReady), errors.Is(err, agent.ErrNoDraft):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "body": body})
}
