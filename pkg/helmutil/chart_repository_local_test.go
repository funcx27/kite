package helmutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zxh326/kite/pkg/model"
	"helm.sh/helm/v4/pkg/getter"
	repo "helm.sh/helm/v4/pkg/repo/v1"
)

// TestLoadRepositoryArchiveFromLocalRepository covers the same download path
// as TestLoadRepositoryArchiveFromRepository but against a local httptest
// chart repository, so it holds without external network access.
func TestLoadRepositoryArchiveFromLocalRepository(t *testing.T) {
	chartArchive := testChartArchive(t, "kite", "0.1.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, err := fmt.Fprintf(w, "apiVersion: v1\nentries:\n  kite:\n    - name: kite\n      version: 0.1.0\n      urls:\n        - kite-0.1.0.tgz\n")
			require.NoError(t, err)
		case "/kite-0.1.0.tgz":
			_, err := w.Write(chartArchive)
			require.NoError(t, err)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repository := model.HelmRepository{
		Name: "kite",
		URL:  server.URL,
	}
	chartRepository, err := repo.NewChartRepository(&repo.Entry{
		Name: repository.Name,
		URL:  repository.URL,
	}, getter.Getters())
	require.NoError(t, err)
	chartRepository.CachePath = t.TempDir()

	indexPath, err := chartRepository.DownloadIndexFile()
	require.NoError(t, err)
	indexFile, err := repo.LoadIndexFile(indexPath)
	require.NoError(t, err)

	entries := indexFile.Entries["kite"]
	require.NotEmpty(t, entries)

	loadedChart, err := LoadRepositoryArchive(repository, entries[0])
	require.NoError(t, err)
	require.Equal(t, "kite", loadedChart.Metadata.Name)
	require.Equal(t, entries[0].Version, loadedChart.Metadata.Version)
}

// testChartArchive builds an in-memory chart archive (tar.gz with a
// top-level <name>/ directory, as a repository would serve it).
func testChartArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     name + "/Chart.yaml",
		Mode:     0o644,
		Size:     int64(len(chartYAML)),
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write([]byte(chartYAML))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}
