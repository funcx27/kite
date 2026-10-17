// Package gitctx turns a git source into a deterministic build-context
// tarball. Cloning happens caller-side (DESIGN.md §6 GitSource): the
// runtime needs no git binary and no egress to the git server.
package gitctx

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httptransport "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/funcx27/kite/modules/buildfleet/internal/dirtar"
)

// Source describes one git build context.
type Source struct {
	URL      string // clone URL (http/https or local path)
	Mode     string // "server" (default, daemon-side fetch) | "runtime" (buildkitd fetches)
	Ref      string // branch, tag or commit SHA; empty = HEAD
	Subdir   string // optional directory inside the repo as context root
	AuthEnv  string // env var holding "user:token" for http(s) basic auth
	Username string // literal basic-auth (API passthrough); used when AuthEnv is empty
	Password string
	// ExpectedCommit, when set, is a pre-resolved commit SHA the checkout
	// must land on. Fetch still clones by Ref — a SHA ref would force a full
	// clone — and fails deterministically when the checked-out HEAD differs,
	// i.e. the ref moved between resolution and clone. Zero value = no check.
	ExpectedCommit string
}

// Result is the materialized context tarball.
type Result struct {
	TarPath string // temp file; caller must remove
	Commit  string // resolved commit SHA
}

// Fetch clones src, checks out the resolved ref, and tars the worktree
// without .git. Tar headers use zero ModTime so the same commit always
// yields the same bytes (and thus the same store digest).
//
// Branch/tag/HEAD clone shallow (Depth 1: a build only needs the tip's
// content); a commit-SHA ref falls back to a full clone because servers
// rarely allow fetching an arbitrary SHA shallow.
func Fetch(ctx context.Context, src Source) (*Result, error) {
	auth, err := basicAuth(src)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "buildfleet-git-*")
	if err != nil {
		return nil, fmt.Errorf("gitctx: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	cloneOpts := &git.CloneOptions{URL: src.URL, Auth: auth}
	if name, shallow, err := planClone(src.URL, src.Ref, auth); err != nil {
		return nil, err
	} else if shallow {
		cloneOpts.Depth = 1
		cloneOpts.ReferenceName = name
	}

	repo, err := git.PlainCloneContext(ctx, dir, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("gitctx: clone %s: %w", src.URL, err)
	}
	commit, err := checkout(repo, src.Ref)
	if err != nil {
		return nil, err
	}
	if src.ExpectedCommit != "" && commit != src.ExpectedCommit {
		shownRef := src.Ref
		if shownRef == "" {
			shownRef = "HEAD"
		}
		return nil, fmt.Errorf("gitctx: %s resolved to %s after %s was pinned (the ref moved during the build; retry)", shownRef, commit, src.ExpectedCommit)
	}

	root := dir
	if src.Subdir != "" {
		root = filepath.Join(dir, filepath.Clean("/"+src.Subdir))
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("gitctx: subdir %q not found", src.Subdir)
		}
	}
	tarPath, err := dirtar.Write(root, ".git")
	if err != nil {
		return nil, err
	}
	return &Result{TarPath: tarPath, Commit: commit}, nil
}

// FileRead is one file's content read from a clone of a source's ref.
type FileRead struct {
	Content []byte
	Commit  string
	// Found reports whether the path named a regular file.
	Found bool
}

// ReadFile clones src (the original ref — never a pre-resolved SHA, which
// planClone would downgrade to a full clone) and reads one worktree file.
// It skips Fetch's tar round-trip: a single file needs no tarball. A path
// that names no regular file is Found=false, not an error.
func ReadFile(ctx context.Context, src Source, name string) (*FileRead, error) {
	auth, err := basicAuth(src)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "buildfleet-git-read-*")
	if err != nil {
		return nil, fmt.Errorf("gitctx: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	cloneOpts := &git.CloneOptions{URL: src.URL, Auth: auth}
	if refName, shallow, err := planClone(src.URL, src.Ref, auth); err != nil {
		return nil, err
	} else if shallow {
		cloneOpts.Depth = 1
		cloneOpts.ReferenceName = refName
	}
	repo, err := git.PlainCloneContext(ctx, dir, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("gitctx: clone %s: %w", src.URL, err)
	}
	commit, err := checkout(repo, src.Ref)
	if err != nil {
		return nil, err
	}

	root := dir
	if src.Subdir != "" {
		root = filepath.Join(dir, filepath.Clean("/"+src.Subdir))
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("gitctx: subdir %q not found", src.Subdir)
		}
	}
	p, rerr := resolveInRoot(root, name)
	if rerr != nil {
		// Missing, not a regular file, or a symlink that loops or tries to
		// leave the context root: buildkit's context read would reject the
		// last two, so they must never resolve to host files here — the
		// preview reads them as absent.
		return &FileRead{Commit: commit}, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("gitctx: read %s: %w", name, err)
	}
	return &FileRead{Content: b, Commit: commit, Found: true}, nil
}

// resolveInRoot resolves a context-relative name inside root, following
// symlinks only while the target and every intermediate path stay within
// root — the confinement buildkit's context read applies, so a hostile
// repo cannot steer the daemon at host files. The resolved path names a
// regular file; any other outcome is an error.
func resolveInRoot(root, name string) (string, error) {
	const maxHops = 32
	hops := 0
	segs := strings.Split(strings.Trim(path.Clean("/"+name), "/"), "/")
	cur := root
	for _, seg := range segs {
		if seg == "." || seg == "" {
			continue
		}
		next := filepath.Join(cur, seg)
		for {
			fi, err := os.Lstat(next)
			if err != nil {
				return "", err
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				break
			}
			hops++
			if hops > maxHops {
				return "", fmt.Errorf("gitctx: too many symlink hops resolving %q", name)
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				return "", fmt.Errorf("gitctx: symlink in %q has absolute target %q", name, target)
			}
			resolved := filepath.Join(filepath.Dir(next), target)
			if !withinRoot(root, resolved) {
				return "", fmt.Errorf("gitctx: symlink in %q escapes the context root", name)
			}
			next = resolved
		}
		cur = next
	}
	// The loop only chased symlinks; the answer itself must be a regular
	// file (this also covers a name that cleaned down to the root).
	fi, err := os.Lstat(cur)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("gitctx: %q is not a regular file", name)
	}
	return cur, nil
}

// withinRoot reports whether p is root or lives under it.
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// planClone decides the clone shape from the ref name via ls-remote:
// branch/tag/HEAD → (reference name, shallow=true); commit SHA → (zero,
// false, full clone); unknown name → error before any transfer.
// If ls-remote itself fails (odd server), we conservatively return a
// full-clone plan and let the clone surface the real error.
func planClone(url, ref string, auth transport.AuthMethod) (plumbing.ReferenceName, bool, error) {
	switch {
	case ref == "" || ref == "HEAD":
		return "", true, nil
	case plumbing.IsHash(ref):
		return "", false, nil
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin", URLs: []string{url},
	})
	refs, err := remote.List(&git.ListOptions{Auth: auth})
	if err != nil {
		return "", false, nil
	}
	for _, r := range refs {
		if r.Name() == plumbing.NewBranchReferenceName(ref) ||
			r.Name() == plumbing.NewTagReferenceName(ref) {
			return r.Name(), true, nil
		}
	}
	return "", false, fmt.Errorf("gitctx: ref %q not found (branch/tag/SHA)", ref)
}

func basicAuth(src Source) (transport.AuthMethod, error) {
	var user, pass string
	switch {
	case src.AuthEnv != "":
		v, ok := os.LookupEnv(src.AuthEnv)
		if !ok {
			return nil, fmt.Errorf("gitctx: env %s not set", src.AuthEnv)
		}
		user, pass, _ = strings.Cut(v, ":")
	case src.Username != "":
		user, pass = src.Username, src.Password
	default:
		return nil, nil
	}
	u, err := url.Parse(src.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("gitctx: auth only supported for http(s) URLs")
	}
	return &httptransport.BasicAuth{Username: user, Password: pass}, nil
}

// checkout resolves ref (branch, tag, SHA or HEAD) and checks it out.
func checkout(repo *git.Repository, ref string) (string, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("gitctx: worktree: %w", err)
	}
	hash, err := resolveRef(repo, ref)
	if err != nil {
		return "", err
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		return "", fmt.Errorf("gitctx: checkout %s: %w", ref, err)
	}
	return hash.String(), nil
}

func resolveRef(repo *git.Repository, ref string) (plumbing.Hash, error) {
	switch {
	case ref == "" || ref == "HEAD":
		head, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitctx: head: %w", err)
		}
		return head.Hash(), nil
	case plumbing.IsHash(ref):
		return plumbing.NewHash(ref), nil
	}
	for _, name := range []plumbing.ReferenceName{
		plumbing.NewBranchReferenceName(ref),
		plumbing.NewTagReferenceName(ref),
	} {
		if r, err := repo.Reference(name, true); err == nil {
			return r.Hash(), nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("gitctx: ref %q not found (branch/tag/SHA)", ref)
}

// lsRemoteTimeout bounds one ls-remote round trip (ListRefs): an
// advertisement is a single response, so a server that cannot answer within
// this window is unreachable for listing purposes.
const lsRemoteTimeout = 10 * time.Second

// ErrNoSuchRef marks an ls-remote that reached the server yet advertised no
// matching branch/tag/HEAD: deterministic, unlike transport failures.
var ErrNoSuchRef = errors.New("no such branch/tag/SHA")

// ErrSourceInvalid marks source configuration no retry can fix (auth that
// cannot apply to the URL shape): deterministic, unlike transport failures.
var ErrSourceInvalid = errors.New("invalid git source")

// ResolveCommit answers ref -> commit SHA with a single ls-remote round
// trip (no clone): used by the runtime git mode, where buildkitd resolves
// the ref internally and never reports the commit back. Best-effort
// metadata — a git server the caller cannot reach must not fail builds, so
// any failure (including the 5s timeout) yields an empty commit; callers
// that need the failure's cause use ResolveCommitErr.
func ResolveCommit(src Source) string {
	commit, _ := ResolveCommitErr(src)
	return commit
}

// ResolveCommitErr is ResolveCommit with the failure surfaced: transport
// errors pass through unwrapped for the caller to classify, while source
// problems no retry can fix (missing ref, inapplicable auth) carry
// ErrNoSuchRef / ErrSourceInvalid.
func ResolveCommitErr(src Source) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if plumbing.IsHash(src.Ref) {
		return src.Ref, nil
	}
	auth, err := basicAuth(src)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSourceInvalid, err)
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin", URLs: []string{src.URL},
	})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", err
	}
	if src.Ref != "" {
		want := []plumbing.ReferenceName{
			plumbing.NewBranchReferenceName(src.Ref),
			plumbing.NewTagReferenceName(src.Ref),
		}
		for _, r := range refs {
			for _, w := range want {
				if r.Name() == w {
					return r.Hash().String(), nil
				}
			}
		}
		return "", fmt.Errorf("gitctx: ref %q: %w", src.Ref, ErrNoSuchRef)
	}
	// Empty ref = HEAD. Some servers (and go-git's local transport)
	// report HEAD as a symbolic ref with a zero hash; fall back to the
	// conventional default branch names.
	for _, r := range refs {
		if r.Name() == plumbing.HEAD && !r.Hash().IsZero() {
			return r.Hash().String(), nil
		}
	}
	for _, name := range []plumbing.ReferenceName{
		plumbing.NewBranchReferenceName("main"),
		plumbing.NewBranchReferenceName("master"),
	} {
		for _, r := range refs {
			if r.Name() == name {
				return r.Hash().String(), nil
			}
		}
	}
	return "", fmt.Errorf("gitctx: HEAD: %w", ErrNoSuchRef)
}

// RefList is one repository's branch/tag advertisement.
type RefList struct {
	Branches []string // refs/heads/* with the prefix stripped
	Tags     []string // refs/tags/* with the prefix stripped
}

// ListRefs answers the repository's full branch/tag advertisement with one
// ls-remote round trip (no clone), using the same ListContext/auth shape as
// ResolveCommitErr. The deadline is capped at lsRemoteTimeout (an earlier
// caller deadline wins) so a hung server cannot hold the caller beyond the
// fast path's budget. Peeled annotated-tag lines (refs/tags/v1^{}) collapse
// into their tag entry.
func ListRefs(ctx context.Context, src Source) (*RefList, error) {
	ctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()
	auth, err := basicAuth(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceInvalid, err)
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin", URLs: []string{src.URL},
	})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return nil, err
	}
	out := &RefList{Branches: []string{}, Tags: []string{}}
	seen := map[string]bool{}
	for _, r := range refs {
		name := strings.TrimSuffix(r.Name().String(), "^{}")
		if b, ok := strings.CutPrefix(name, "refs/heads/"); ok {
			if !seen[name] {
				seen[name] = true
				out.Branches = append(out.Branches, b)
			}
		} else if t, ok := strings.CutPrefix(name, "refs/tags/"); ok {
			if !seen[name] {
				seen[name] = true
				out.Tags = append(out.Tags, t)
			}
		}
	}
	sort.Strings(out.Branches)
	sort.Strings(out.Tags)
	return out, nil
}
