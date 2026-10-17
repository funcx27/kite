package fleet

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/funcx27/kite/modules/buildfleet/builder"
)

// probeDest is the absolute destination the probe's inline COPY lands the
// file at, so the tar round-trip can identify it regardless of the source
// path's basename (and a directory source stays distinguishable: its
// contents land under probeDest/, never at the exact entry).
const probeDest = "/probe-file"

// ProbeResult is one file fetched from a request's build context.
type ProbeResult struct {
	// Content holds the file's bytes; nil when Exists is false.
	Content []byte
	// Commit is the git commit the probe actually resolved and read (the
	// same commitExtractor harvest Build uses); empty when the events
	// carried no ls-remote line.
	Commit string
	// Exists reports whether the probed path named a file in the context.
	Exists bool
	// Attempts, RuntimeID and Provider mirror BuildResult.
	Attempts  int
	RuntimeID string
	Provider  string
}

// RefsResult is a repository's branch/tag advertisement as observed by a
// runtime probe.
type RefsResult struct {
	Branches []string
	Tags     []string
	Provider string
}

// probeMetaChars are the characters the inlined COPY would treat as syntax
// (wildcards, env expansion, quoting) rather than the literal filename a
// build's dockerfile read uses; a leading dash parses as a COPY flag.
const probeMetaChars = `*?[$\"'`

// validProbePath reports whether file can be inlined into the probe's COPY
// verbatim, so its resolution matches a real build's filename lookup.
func validProbePath(file string) bool {
	return !strings.ContainsAny(file, probeMetaChars) && !strings.HasPrefix(file, "-")
}

// ProbeFile fetches one context-relative file by running a real solve with
// the request's source/context assembly reused verbatim: only the
// Dockerfile (inlined as "FROM scratch\nCOPY <path> /probe-file", so
// buildkit resolves the path against the exact context root a build would
// use, Subdir included) and the exporter (tar to a temp file, no push) are
// swapped. A path that names no file fails the COPY and reports
// Exists=false rather than an error; other solve errors surface normally.
func (f *Fleet) ProbeFile(ctx context.Context, req Request, file string, opts ...BuildOption) (*ProbeResult, error) {
	var cfg buildConfig
	for _, o := range opts {
		o(&cfg)
	}
	if req.Git == nil || req.Git.Mode != "runtime" {
		return nil, fmt.Errorf("fleet: probe requires a runtime-mode git source")
	}
	if !validProbePath(file) {
		return nil, fmt.Errorf("fleet: probe path %q contains characters the inlined COPY would reinterpret (wildcard, env, quote or flag syntax)", file)
	}
	tf, err := os.CreateTemp("", "buildfleet-probe-*.tar")
	if err != nil {
		return nil, fmt.Errorf("fleet: probe tar: %w", err)
	}
	tarPath := tf.Name()
	if err := tf.Close(); err != nil {
		os.Remove(tarPath)
		return nil, fmt.Errorf("fleet: probe tar: %w", err)
	}
	defer os.Remove(tarPath)

	run, err := f.runSolve(ctx, req, &cfg, func(in *builder.SolveInput, _ string) {
		in.DockerfileContent = "FROM scratch\nCOPY " + file + " " + probeDest
		in.OutputRef = "" // a probe never pushes
		in.CacheRef = ""  // scratch + one COPY has no layers worth caching
		in.TarOut = tarPath
	})
	if err != nil {
		if probeMissingPath(err, file) {
			return &ProbeResult{Commit: cfg.commit}, nil
		}
		return nil, err
	}
	content, found, err := readProbeTar(tarPath)
	if err != nil {
		return nil, err
	}
	return &ProbeResult{
		Content:   content,
		Commit:    cfg.commit,
		Exists:    found,
		Attempts:  run.attempts,
		RuntimeID: run.runtimeID,
		Provider:  run.provider,
	}, nil
}

// probeRefsPattern is the ref the listing probe requests. Buildkit scopes
// its git-source ls-remote to the requested ref (source/git resolveMetadata:
// "ls-remote -- remote <ref> <ref>^{}"), so an empty ref only ever streams
// the default branch. A glob makes the server advertise every refs/* entry,
// and the resolution then fails deterministically ("repository does not
// contain ref refs/*") before any fetch — the streamed advertisement, not
// the solve, is the probe's answer.
const probeRefsPattern = "refs/*"

// ProbeRefs lists a repository's branches and tags through a runtime probe:
// the "[internal] load git source" vertex streams buildkitd's ls-remote
// output for the glob ref, harvested by the same extractor Build uses. The
// solve itself always fails at ref resolution (no fetch, no transfer), so a
// non-empty harvest is returned even alongside the error; only a probe that
// advertised nothing fails.
func (f *Fleet) ProbeRefs(ctx context.Context, req Request, opts ...BuildOption) (*RefsResult, error) {
	var cfg buildConfig
	for _, o := range opts {
		o(&cfg)
	}
	if req.Git == nil || req.Git.Mode != "runtime" {
		return nil, fmt.Errorf("fleet: ref listing requires a runtime-mode git source")
	}
	probeReq := req
	src := *req.Git
	src.Ref, src.ExpectedCommit, src.Subdir = probeRefsPattern, "", ""
	probeReq.Git = &src
	provider := ""
	_, err := f.runSolve(ctx, probeReq, &cfg, func(in *builder.SolveInput, owner string) {
		// COPY . forces the context fetch path: with an inlined FROM
		// scratch and no COPY, buildkit would skip loading the git source.
		in.DockerfileContent = "FROM scratch\nCOPY . /"
		in.OutputRef = "" // a probe never pushes
		in.CacheRef = ""  // scratch + one COPY has no layers worth caching
		in.TarOut = ""    // and exports nothing
		provider = owner
	})
	branches, tags := cfg.advertisement()
	if len(branches)+len(tags) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("fleet: probe advertised no refs (no git-source events observed)")
	}
	return &RefsResult{Branches: branches, Tags: tags, Provider: provider}, nil
}

// advertisement converts the harvested ls-remote lines into deduped, sorted
// branch/tag lists (prefixes stripped, peeled ^{} lines collapsed). Empty
// lists mean the events carried no advertisement.
func (cfg *buildConfig) advertisement() (branches, tags []string) {
	cfg.commitMu.Lock()
	defer cfg.commitMu.Unlock()
	bset, tset := map[string]bool{}, map[string]bool{}
	for _, adv := range cfg.refAds {
		name := strings.TrimSuffix(adv.Ref, "^{}")
		if b, ok := strings.CutPrefix(name, "refs/heads/"); ok {
			bset[b] = true
		} else if t, ok := strings.CutPrefix(name, "refs/tags/"); ok {
			tset[t] = true
		}
	}
	for b := range bset {
		branches = append(branches, b)
	}
	for t := range tset {
		tags = append(tags, t)
	}
	sort.Strings(branches)
	sort.Strings(tags)
	return branches, tags
}

// readProbeTar extracts the probe destination entry from a solve's tar
// export; found=false when the entry is absent (e.g. the probed path named
// a directory, whose contents land under probeDest/ instead).
func readProbeTar(tarPath string) (content []byte, found bool, err error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, false, fmt.Errorf("fleet: probe tar: %w", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("fleet: probe tar: %w", err)
		}
		if path.Clean("/"+h.Name) != probeDest || h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, false, fmt.Errorf("fleet: probe tar: %w", err)
		}
		return b, true, nil
	}
}

// probeMissingPath reports whether a probe solve error is the COPY source's
// own "not found". Buildkit emits two shapes, both carrying the cleaned
// absolute path: contenthash wraps containerd's ErrNotFound with the quoted
// path (`"/dir/file": not found`), and the file backend's zero wildcard
// match reports it unquoted (`/dir/file not found`,
// solver/llbsolver/file/backend.go). Matching the path keeps unrelated
// not-found errors (e.g. a missing repository) classified as failures.
func probeMissingPath(err error, file string) bool {
	msg := err.Error()
	abs := path.Clean("/" + file)
	return strings.Contains(msg, fmt.Sprintf("%q: not found", abs)) ||
		strings.Contains(msg, abs+" not found")
}
