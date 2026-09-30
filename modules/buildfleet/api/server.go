// Package api serves the buildfleet SDK over Connect: native gRPC,
// gRPC-Web and plain HTTP/JSON on one handler, all server-streaming
// live build events.
package api

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	"github.com/funcx27/kite/modules/buildfleet/builder"
	"github.com/funcx27/kite/modules/buildfleet/creds"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/funcx27/kite/modules/buildfleet/httpctx"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"

	"github.com/moby/buildkit/client"

	"connectrpc.com/connect"
)

// Server implements buildfleet.v1.BuildService.
type Server struct {
	fleet *fleet.Fleet
	store store.Store
	// cacheStat, when set, walks each cache-enabled provider's registry.
	cacheStat func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error)
}

func NewServer(f *fleet.Fleet, st store.Store, opts ...ServerOption) *Server {
	s := &Server{fleet: f, store: st}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServerOption customizes the API server.
type ServerOption func(*Server)

// WithCacheStat injects the per-provider cache usage provider (name ->
// snapshot); empty map means no cache registries.
func WithCacheStat(fn func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error)) ServerOption {
	return func(s *Server) { s.cacheStat = fn }
}

// Build runs one build and streams vertex/log events as they happen,
// then a terminal result or error event.
func (s *Server) Build(ctx context.Context, req *connect.Request[buildfleetv1.BuildRequest], stream *connect.ServerStream[buildfleetv1.BuildEvent]) error {
	freq, err := s.toFleetRequest(ctx, req.Msg)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	// stream.Send is only legal on the handler goroutine, so the build
	// runs in the background and events funnel through evCh.
	evCh := make(chan *buildfleetv1.BuildEvent, 16)
	type outcome struct {
		res *fleet.BuildResult
		err error
	}
	outCh := make(chan outcome, 1)
	go func() {
		res, err := s.fleet.Build(ctx, freq, fleet.WithProgress(func(ev builder.Event) bool {
			out, ok := toAPIEvent(ev)
			if !ok {
				return true
			}
			select {
			case evCh <- out:
				return true
			case <-ctx.Done():
				return false
			}
		}))
		outCh <- outcome{res: res, err: err}
	}()

	for {
		select {
		case ev := <-evCh:
			if err := stream.Send(ev); err != nil {
				return err
			}
		case o := <-outCh:
		drain:
			for {
				select { // drain queued events before the terminal one
				case ev := <-evCh:
					if err := stream.Send(ev); err != nil {
						return err
					}
					continue
				default:
					break drain
				}
			}
			if o.err != nil {
				return stream.Send(newErrorEvent(o.err))
			}
			return stream.Send(&buildfleetv1.BuildEvent{Event: &buildfleetv1.BuildEvent_Result{
				Result: &buildfleetv1.BuildResult{
					Digest:    o.res.Digest,
					ImageRef:  o.res.ImageRef,
					Attempts:  int32(o.res.Attempts),
					Ref:       o.res.Ref,
					Commit:    o.res.Commit,
					RuntimeId: o.res.RuntimeID,
					Provider:  o.res.Provider,
				},
			}})
		}
	}
}

func toAPIEvent(ev builder.Event) (*buildfleetv1.BuildEvent, bool) {
	switch {
	case ev.Vertex != nil:
		return &buildfleetv1.BuildEvent{Event: &buildfleetv1.BuildEvent_Vertex{
			Vertex: &buildfleetv1.VertexEvent{
				Digest: string(ev.Vertex.Digest),
				Name:   ev.Vertex.Name,
				Status: vertexStatus(ev.Vertex),
			},
		}}, true
	case ev.Log != nil:
		return &buildfleetv1.BuildEvent{Event: &buildfleetv1.BuildEvent_Log{
			Log: &buildfleetv1.LogEvent{
				Vertex: string(ev.Log.Vertex),
				Data:   ev.Log.Data,
			},
		}}, true
	case ev.Start != nil:
		return &buildfleetv1.BuildEvent{Event: &buildfleetv1.BuildEvent_Runtime{
			Runtime: &buildfleetv1.RuntimeEvent{
				RuntimeId: ev.Start.RuntimeID,
				Provider:  ev.Start.Provider,
				Addr:      ev.Start.Addr,
				Attempt:   int32(ev.Start.Attempt),
			},
		}}, true
	default:
		return nil, false
	}
}

func newErrorEvent(err error) *buildfleetv1.BuildEvent {
	return &buildfleetv1.BuildEvent{Event: &buildfleetv1.BuildEvent_Error{
		Error: err.Error(),
	}}
}

func (s *Server) toFleetRequest(ctx context.Context, msg *buildfleetv1.BuildRequest) (fleet.Request, error) {
	req := fleet.Request{
		Dockerfile: msg.GetDockerfile(),
		Args:       msg.GetArgs(),
		Platforms:  msg.GetPlatforms(),
		OutputRef:  msg.GetOutputRef(),
	}
	req.DockerfileContent = msg.GetDockerfileContent()
	if req.Dockerfile == "" {
		req.Dockerfile = "Dockerfile"
	}
	switch c := msg.GetContext().(type) {
	case *buildfleetv1.BuildRequest_ContextTar:
		if s.store == nil {
			return req, fmt.Errorf("server has no store configured for tar upload")
		}
		dig, err := s.store.Push(ctx, bytes.NewReader(c.ContextTar))
		if err != nil {
			return req, fmt.Errorf("push context: %w", err)
		}
		req.ContextDigest = dig
	case *buildfleetv1.BuildRequest_ContextDigest:
		req.ContextDigest = c.ContextDigest
	case *buildfleetv1.BuildRequest_Git:
		g := c.Git
		if g.GetUrl() == "" {
			return req, fmt.Errorf("git.url is required")
		}
		req.Git = &gitctx.Source{
			URL:     g.GetUrl(),
			Ref:     g.GetRef(),
			Subdir:  g.GetSubdir(),
			AuthEnv: g.GetAuthEnv(),
			Mode:    g.GetMode(),
		}
	case *buildfleetv1.BuildRequest_Http:
		h := c.Http
		if h.GetUrl() == "" {
			return req, fmt.Errorf("http.url is required")
		}
		req.HTTP = &httpctx.Source{
			URL:     h.GetUrl(),
			AuthEnv: h.GetAuthEnv(),
			Mode:    h.GetMode(),
		}
	default:
		return req, fmt.Errorf("context (tar, digest, git or http) is required")
	}
	for _, c := range msg.GetCredentials() {
		kind := creds.Kind(c.GetKind())
		if kind != creds.Registry && kind != creds.Git {
			return req, fmt.Errorf("credential kind %q must be registry or git", c.GetKind())
		}
		if c.GetMatch() == "" {
			return req, fmt.Errorf("credential match is required")
		}
		req.Creds = append(req.Creds, creds.Credential{
			Kind:   kind,
			Match:  c.GetMatch(),
			Direct: &creds.Secret{Username: c.GetUsername(), Secret: c.GetPassword()},
		})
	}
	if r := msg.GetResources(); r != nil {
		req.Resources.CPU = r.GetCpu()
		if mem := r.GetMemory(); mem != "" {
			b, err := resource.ParseQuantity(mem)
			if err != nil {
				return req, fmt.Errorf("resources.memory: %w", err)
			}
			req.Resources.Memory = b
		}
		if es := r.GetEphemeralStorage(); es != "" {
			b, err := resource.ParseQuantity(es)
			if err != nil {
				return req, fmt.Errorf("resources.ephemeral_storage: %w", err)
			}
			req.Resources.EphemeralStorage = b
		}
	}
	req.Provider = msg.GetProvider()
	return req, nil
}

// vertexStatus derives a display status from lifecycle timestamps.
func vertexStatus(v *client.Vertex) string {
	switch {
	case v.Error != "":
		return "failed"
	case v.Completed != nil && v.Cached:
		return "cached"
	case v.Completed != nil:
		return "complete"
	case v.Started != nil:
		return "running"
	default:
		return "pending"
	}
}

// Status reports the runtime pool state and in-flight builds.
func (s *Server) Status(ctx context.Context, req *connect.Request[buildfleetv1.StatusRequest]) (*connect.Response[buildfleetv1.StatusResponse], error) {
	resp := &buildfleetv1.StatusResponse{}
	for _, r := range s.fleet.Stats() {
		resp.Runtimes = append(resp.Runtimes, &buildfleetv1.RuntimeStatus{
			Id:          r.ID,
			State:       string(r.State),
			Addr:        r.Addr,
			Provider:    r.Owner,
			Cpu:         r.CPU,
			MemoryBytes: int64(r.Memory),
			IdleSeconds: int64(r.IdleFor.Seconds()),
		})
	}
	for _, t := range s.fleet.Tasks() {
		resp.Tasks = append(resp.Tasks, &buildfleetv1.TaskStatus{
			RuntimeId:   t.RuntimeID,
			Source:      t.Source,
			OutputRef:   t.OutputRef,
			StartedUnix: t.Started.Unix(),
			Attempt:     int32(t.Attempt),
		})
	}
	return connect.NewResponse(resp), nil
}

// CacheStat reports per-project cache usage for every cache registry.
func (s *Server) CacheStat(ctx context.Context, req *connect.Request[buildfleetv1.CacheStatRequest]) (*connect.Response[buildfleetv1.CacheStatResponse], error) {
	if s.cacheStat == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no cache registries configured"))
	}
	snaps, err := s.cacheStat(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &buildfleetv1.CacheStatResponse{}
	for provider, snap := range snaps {
		resp.TotalBytes += snap.TotalBytes
		for _, p := range snap.Projects {
			resp.Projects = append(resp.Projects, &buildfleetv1.CacheProjectStat{
				Provider:       provider,
				Project:        p.Project,
				Tags:           int32(p.Tags),
				Layers:         int32(p.Layers),
				ExclusiveBytes: p.Exclusive,
				SharedBytes:    p.Shared,
			})
		}
	}
	sort.Slice(resp.Projects, func(i, j int) bool {
		if resp.Projects[i].Provider != resp.Projects[j].Provider {
			return resp.Projects[i].Provider < resp.Projects[j].Provider
		}
		return resp.Projects[i].Project < resp.Projects[j].Project
	})
	return connect.NewResponse(resp), nil
}
