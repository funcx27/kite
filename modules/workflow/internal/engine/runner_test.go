package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

type stubBuildDeps struct {
	authorized bool
	res        exec.BuildResult
	err        error
	gotReq     exec.BuildRequest
	called     bool
}

func (s *stubBuildDeps) AuthorizeUser(uint, string, string, string, string) bool { return s.authorized }

func (s *stubBuildDeps) BuildImage(_ context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error) {
	s.called = true
	s.gotReq = req
	if s.err != nil {
		return exec.BuildResult{}, s.err
	}
	if onLog != nil {
		onLog("build log")
	}
	return s.res, nil
}

func (s *stubBuildDeps) ProviderFor(string) (string, bool) { return "", false }

func (s *stubBuildDeps) DefaultRegistry() (string, string, bool) { return "", "", false }

func TestBuildRunnerRun(t *testing.T) {
	tests := []struct {
		name       string
		props      spec.Props
		deps       stubBuildDeps
		wantErr    string
		wantDocker string
		wantSource func(t *testing.T, req exec.BuildRequest)
		wantOut    map[string]any
	}{
		{
			name:    "unauthorized user fails the node before any build",
			props:   spec.Props{"git": map[string]any{"url": "u"}, "outputRef": "reg/web:v1"},
			deps:    stubBuildDeps{authorized: false},
			wantErr: "does not have permission to create builds",
		},
		{
			name:    "missing source",
			props:   spec.Props{"outputRef": "reg/web:v1"},
			deps:    stubBuildDeps{authorized: true},
			wantErr: "build node needs a git or http source",
		},
		{
			name:    "missing outputRef",
			props:   spec.Props{"git": map[string]any{"url": "u"}},
			deps:    stubBuildDeps{authorized: true},
			wantErr: "build node needs outputRef",
		},
		{
			name:       "git source with default dockerfile",
			props:      spec.Props{"git": map[string]any{"url": "https://git.acme/web.git"}, "outputRef": "reg/web:v1"},
			deps:       stubBuildDeps{authorized: true, res: exec.BuildResult{ImageRef: "reg/web:v1", Digest: "sha256:a"}},
			wantDocker: "Dockerfile",
			wantSource: func(t *testing.T, req exec.BuildRequest) {
				if req.Git == nil || req.Git.URL != "https://git.acme/web.git" || req.HTTP != nil {
					t.Errorf("got git source %+v, http %+v", req.Git, req.HTTP)
				}
			},
			wantOut: map[string]any{
				"imageRef":           "reg/web:v1",
				"digest":             "sha256:a",
				"imageRefWithDigest": "reg/web@sha256:a",
			},
		},
		{
			name:       "http source keeps explicit dockerfile and forwards logs",
			props:      spec.Props{"http": map[string]any{"url": "https://acme/ctx.tar.gz"}, "outputRef": "reg/web:v1", "dockerfile": "build.Dockerfile"},
			deps:       stubBuildDeps{authorized: true, res: exec.BuildResult{ImageRef: "reg/web:v1"}},
			wantDocker: "build.Dockerfile",
			wantSource: func(t *testing.T, req exec.BuildRequest) {
				if req.HTTP == nil || req.HTTP.URL != "https://acme/ctx.tar.gz" || req.Git != nil {
					t.Errorf("got git source %+v, http %+v", req.Git, req.HTTP)
				}
			},
			wantOut: map[string]any{"imageRef": "reg/web:v1", "digest": ""},
		},
		{
			name:    "unknown props field rejected at run time",
			props:   spec.Props{"git": map[string]any{"url": "u"}, "outputRef": "r", "bogus": 1},
			deps:    stubBuildDeps{authorized: true},
			wantErr: "decode build props",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := buildRunner{deps: &tt.deps}
			var logs []string
			outputs, err := runner.Run(context.Background(), tt.props, store.WorkflowRun{TriggeredBy: 7}, func(p string) { logs = append(logs, p) })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Run err = %v, want containing %q", err, tt.wantErr)
				}
				if tt.deps.called {
					t.Error("BuildImage called despite failing preconditions")
				}
				return
			}
			if err != nil {
				t.Fatalf("Run err = %v", err)
			}
			if tt.deps.gotReq.Dockerfile != tt.wantDocker {
				t.Errorf("Dockerfile = %q, want %q", tt.deps.gotReq.Dockerfile, tt.wantDocker)
			}
			if tt.wantSource != nil {
				tt.wantSource(t, tt.deps.gotReq)
			}
			if !reflect.DeepEqual(outputs, tt.wantOut) {
				t.Errorf("outputs = %v, want %v", outputs, tt.wantOut)
			}
			if len(logs) != 1 || logs[0] != "build log" {
				t.Errorf("logs = %v, want [build log]", logs)
			}
		})
	}
}

func TestBuildRunnerBuildErrorPropagates(t *testing.T) {
	deps := &stubBuildDeps{authorized: true, err: context.DeadlineExceeded}
	runner := buildRunner{deps: deps}
	_, err := runner.Run(context.Background(),
		spec.Props{"git": map[string]any{"url": "u"}, "outputRef": "r"}, store.WorkflowRun{}, nil)
	if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Run err = %v, want the executor error propagated", err)
	}
}

type stubDeployDeps struct {
	res    exec.DeployResult
	err    error
	gotReq exec.DeployRequest
}

func (s *stubDeployDeps) DeployApp(_ context.Context, req exec.DeployRequest, _ func(string)) (exec.DeployResult, error) {
	s.gotReq = req
	return s.res, s.err
}

func TestDeployRunnerRun(t *testing.T) {
	run := store.WorkflowRun{TriggeredBy: 7}
	tests := []struct {
		name    string
		props   spec.Props
		deps    stubDeployDeps
		wantErr string
		wantReq exec.DeployRequest
		wantOut map[string]any
	}{
		{
			name:    "missing cluster",
			props:   spec.Props{"app": "web"},
			deps:    stubDeployDeps{},
			wantErr: "deploy node needs space, app and cluster",
		},
		{
			name:    "missing space",
			props:   spec.Props{"app": "web", "cluster": "prod"},
			deps:    stubDeployDeps{},
			wantErr: "deploy node needs space, app and cluster",
		},
		{
			name:  "success parses preview json and carries operator identity",
			props: spec.Props{"space": "team-a", "app": "web", "cluster": "prod", "image": "reg/web@sha256:a"},
			deps: stubDeployDeps{res: exec.DeployResult{
				Revision: 3,
				Preview:  json.RawMessage(`[{"kind":"Deployment"}]`),
			}},
			wantReq: exec.DeployRequest{Space: "team-a", App: "web", Cluster: "prod", Image: "reg/web@sha256:a", OperatorID: 7},
			wantOut: map[string]any{"revision": 3, "preview": []any{map[string]any{"kind": "Deployment"}}},
		},
		{
			name:    "invalid preview stays raw",
			props:   spec.Props{"space": "team-a", "app": "web", "cluster": "prod"},
			deps:    stubDeployDeps{res: exec.DeployResult{Revision: 1, Preview: json.RawMessage("not-json")}},
			wantReq: exec.DeployRequest{Space: "team-a", App: "web", Cluster: "prod", OperatorID: 7},
			wantOut: map[string]any{"revision": 1, "preview": json.RawMessage("not-json")},
		},
		{
			name:    "empty preview stays typed-nil raw",
			props:   spec.Props{"space": "team-a", "app": "web", "cluster": "prod"},
			deps:    stubDeployDeps{res: exec.DeployResult{Revision: 2}},
			wantReq: exec.DeployRequest{Space: "team-a", App: "web", Cluster: "prod", OperatorID: 7},
			wantOut: map[string]any{"revision": 2, "preview": json.RawMessage(nil)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := deployRunner{deps: &tt.deps}
			outputs, err := runner.Run(context.Background(), tt.props, run, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Run err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run err = %v", err)
			}
			if tt.deps.gotReq != tt.wantReq {
				t.Errorf("DeployRequest = %+v, want %+v", tt.deps.gotReq, tt.wantReq)
			}
			if !reflect.DeepEqual(outputs, tt.wantOut) {
				t.Errorf("outputs = %v, want %v", outputs, tt.wantOut)
			}
		})
	}
}

type stubNotifyDeps struct {
	err    error
	gotReq exec.NotifyRequest
}

func (s *stubNotifyDeps) Notify(_ context.Context, req exec.NotifyRequest) error {
	s.gotReq = req
	return s.err
}

func TestNotifyRunnerRun(t *testing.T) {
	tests := []struct {
		name        string
		props       spec.Props
		deps        stubNotifyDeps
		wantErr     string
		wantPayload string
	}{
		{
			name:    "missing url",
			props:   spec.Props{},
			wantErr: "notify node needs url",
		},
		{
			name:        "plain text becomes text field",
			props:       spec.Props{"url": "https://hook.acme/x", "payloadTemplate": "run done"},
			wantPayload: `{"text":"run done"}`,
		},
		{
			name:        "valid json template sent as-is",
			props:       spec.Props{"url": "https://hook.acme/x", "payloadTemplate": `{"a":1}`},
			wantPayload: `{"a":1}`,
		},
		{
			name:        "empty template becomes empty object",
			props:       spec.Props{"url": "https://hook.acme/x"},
			wantPayload: `{}`,
		},
		{
			name:    "executor error wrapped with notify prefix",
			props:   spec.Props{"url": "https://hook.acme/x"},
			deps:    stubNotifyDeps{err: context.Canceled},
			wantErr: "notify: context canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := notifyRunner{deps: &tt.deps}
			outputs, err := runner.Run(context.Background(), tt.props, store.WorkflowRun{}, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Run err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run err = %v", err)
			}
			if string(tt.deps.gotReq.Payload) != tt.wantPayload {
				t.Errorf("payload = %s, want %s", tt.deps.gotReq.Payload, tt.wantPayload)
			}
			if tt.deps.gotReq.URL != "https://hook.acme/x" {
				t.Errorf("url = %q", tt.deps.gotReq.URL)
			}
			if delivered, ok := outputs["delivered"].(bool); !ok || !delivered {
				t.Errorf("outputs = %v, want delivered true", outputs)
			}
		})
	}
}

func TestBuildOutputsDigestShaping(t *testing.T) {
	tests := []struct {
		name     string
		res      exec.BuildResult
		wantRef  string
		wantNone bool
	}{
		{"registry port keeps host colon", exec.BuildResult{ImageRef: "reg:5000/web:tag", Digest: "sha256:b"}, "reg:5000/web@sha256:b", false},
		{"no digest omits composed ref", exec.BuildResult{ImageRef: "reg/web:v1"}, "", true},
		{"no image ref omits composed ref", exec.BuildResult{Digest: "sha256:a"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := buildOutputs(tt.res)
			got, ok := out["imageRefWithDigest"]
			if tt.wantNone {
				if ok {
					t.Errorf("imageRefWithDigest = %v, want absent", got)
				}
				return
			}
			if got != tt.wantRef {
				t.Errorf("imageRefWithDigest = %v, want %v", got, tt.wantRef)
			}
		})
	}
}
