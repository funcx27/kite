package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

// CacheStat walks the cache registry API over a temporary port-forward
// and attributes blob bytes to projects: a digest referenced by exactly
// one project counts as exclusive, cross-project digests count as
// shared for each (the registry stores them once; TotalBytes is the
// deduplicated sum).
func (p *Provider) CacheStat(ctx context.Context) (*runtime.CacheStatSnapshot, error) {
	if p.cfg.CacheRegistry == nil {
		return nil, fmt.Errorf("k8s cache stat: no cache registry configured")
	}
	pods, err := p.cfg.Clientset.CoreV1().Pods(p.cfg.Namespace).
		List(ctx, metav1.ListOptions{LabelSelector: "buildfleet=cache-registry"})
	if err != nil {
		return nil, fmt.Errorf("k8s cache stat: list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("k8s cache stat: cache registry pod not found")
	}
	port, stop, err := p.forwardTo(pods.Items[0].Name, 5000)
	if err != nil {
		return nil, err
	}
	defer close(stop)
	return statRegistry(ctx, fmt.Sprintf("http://127.0.0.1:%d", port), cacheRepoPath)
}

// statRegistry implements the API walk; split from CacheStat for tests.
func statRegistry(ctx context.Context, base, repoPrefix string) (*runtime.CacheStatSnapshot, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	manifestAccept := "application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"

	var cat struct {
		Repositories []string `json:"repositories"`
	}
	if err := getJSON(ctx, c, base+"/v2/_catalog", &cat); err != nil {
		return nil, fmt.Errorf("cache stat: catalog: %w", err)
	}

	size := map[string]int64{}    // digest -> bytes
	refs := map[string][]string{} // digest -> projects
	snap := &runtime.CacheStatSnapshot{}
	for _, repo := range cat.Repositories {
		if !strings.HasPrefix(repo, repoPrefix+"/") {
			continue
		}
		project := strings.TrimPrefix(repo, repoPrefix+"/")
		ps := runtime.ProjectStat{Project: project}

		var tags struct {
			Tags []string `json:"tags"`
		}
		if err := getJSON(ctx, c, base+"/v2/"+repo+"/tags/list", &tags); err != nil {
			return nil, fmt.Errorf("cache stat: tags %s: %w", repo, err)
		}
		ps.Tags = len(tags.Tags)
		seen := map[string]bool{}
		for _, tag := range tags.Tags {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
				fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, tag), nil)
			req.Header.Set("Accept", manifestAccept)
			resp, err := c.Do(req)
			if err != nil {
				return nil, fmt.Errorf("cache stat: manifest %s:%s: %w", repo, tag, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				continue
			}
			var m struct {
				Config struct {
					Digest string `json:"digest"`
				} `json:"config"`
				Layers []struct {
					Digest string `json:"digest"`
				} `json:"layers"`
			}
			if err := json.Unmarshal(body, &m); err != nil {
				return nil, fmt.Errorf("cache stat: manifest %s:%s: %w", repo, tag, err)
			}
			digests := []string{m.Config.Digest}
			for _, l := range m.Layers {
				digests = append(digests, l.Digest)
			}
			for _, d := range digests {
				if d == "" || seen[d] {
					continue
				}
				seen[d] = true
				if _, ok := size[d]; !ok {
					n, err := blobSize(ctx, c, base, repo, d)
					if err != nil {
						return nil, err
					}
					size[d] = n
				}
				refs[d] = append(refs[d], project)
			}
		}
		ps.Layers = len(seen)
		snap.Projects = append(snap.Projects, ps)
	}

	for d, projects := range refs {
		if len(projects) == 1 {
			for i := range snap.Projects {
				if snap.Projects[i].Project == projects[0] {
					snap.Projects[i].Exclusive += size[d]
				}
			}
		} else {
			for _, prj := range projects {
				for i := range snap.Projects {
					if snap.Projects[i].Project == prj {
						snap.Projects[i].Shared += size[d]
					}
				}
			}
		}
	}
	for _, n := range size {
		snap.TotalBytes += n
	}
	return snap, nil
}

func blobSize(ctx context.Context, c *http.Client, base, repo, digest string) (int64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead,
		fmt.Sprintf("%s/v2/%s/blobs/%s", base, repo, digest), nil)
	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cache stat: blob %s: %w", digest, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("cache stat: blob %s: http %d", digest, resp.StatusCode)
	}
	return resp.ContentLength, nil
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}
