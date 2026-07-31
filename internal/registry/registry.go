package registry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	Separator      = "__"
	connectTimeout = 10 * time.Second
)

type UpstreamConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type upstream struct {
	name     string
	session  *mcp.ClientSession
	tools    []*mcp.Tool
	protocol string
	caps     *mcp.ServerCapabilities
	err      error
}

// Route says where a namespaced tool call should go.
type Route struct {
	Session    *mcp.ClientSession
	RemoteName string
	Upstream   string
}

type Registry struct {
	mu        sync.RWMutex
	upstreams map[string]*upstream
	routes    map[string]Route
	catalog   []*mcp.Tool
}

func New() *Registry {
	return &Registry{
		upstreams: map[string]*upstream{},
		routes:    map[string]Route{},
	}
}

func (r *Registry) Connect(ctx context.Context, cfgs []UpstreamConfig) {
	results := make([]*upstream, len(cfgs))
	var wg sync.WaitGroup
	for i, c := range cfgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = dial(ctx, c)
		}()
	}
	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range results {
		if u.err != nil {
			log.Printf("upstream %q DEGRADED: %v", u.name, u.err)
		} else {
			log.Printf("upstream %q: %d tools, protocol %s", u.name, len(u.tools), u.protocol)
		}
		r.upstreams[u.name] = u
	}
	r.rebuildLocked()
}

func dial(ctx context.Context, c UpstreamConfig) *upstream {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	u := &upstream{name: c.Name}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway", Version: "v0.0.1"}, nil)
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Stderr = os.Stderr

	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		u.err = fmt.Errorf("connect: %w", err)
		return u
	}
	u.session = session

	if init := session.InitializeResult(); init != nil {
		u.protocol = init.ProtocolVersion
		u.caps = init.Capabilities
	}

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		u.err = fmt.Errorf("list tools: %w", err)
		return u
	}
	u.tools = res.Tools
	return u
}

// rebuildLocked recomputes catalog and routes. Caller must hold r.mu.
func (r *Registry) rebuildLocked() {
	r.catalog = nil
	r.routes = map[string]Route{}

	for _, u := range r.upstreams {
		if u.err != nil {
			continue
		}
		for _, t := range u.tools {
			local := *t
			local.Name = u.name + Separator + t.Name
			r.catalog = append(r.catalog, &local)
			r.routes[local.Name] = Route{
				Session:    u.session,
				RemoteName: t.Name,
				Upstream:   u.name,
			}
		}
	}

	sort.Slice(r.catalog, func(i, j int) bool {
		return r.catalog[i].Name < r.catalog[j].Name
	})
}

func (r *Registry) Lookup(name string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.routes[name]
	return rt, ok
}

func (r *Registry) Catalog() []*mcp.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*mcp.Tool, len(r.catalog))
	copy(out, r.catalog)
	return out
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, u := range r.upstreams {
		if u.session != nil {
			if err := u.session.Close(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", u.name, err))
			}
		}
	}
	return errors.Join(errs...)
}