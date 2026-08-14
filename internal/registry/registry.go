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
	cfg      UpstreamConfig
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

	closed  chan struct{}
	closeOnce sync.Once
	wg      sync.WaitGroup
	onChange func(name string)
}

func New() *Registry {
	return &Registry{
		upstreams: map[string]*upstream{},
		routes:    map[string]Route{},
		closed:    make(chan struct{}),
	}
}

func (r *Registry) Connect(ctx context.Context, cfgs []UpstreamConfig) {
	r.mu.Lock()
	for _, u := range r.upstreams {
		if u.session != nil {
			u.session.Close()
		}
	}
	r.upstreams = map[string]*upstream{}
	r.mu.Unlock()

	onChange := func(name string) {
		go func() {
			if err := r.Refresh(context.Background(), name); err != nil {
				log.Printf("refresh %q: %v", name, err)
			}
		}()
	}

	results := make([]*upstream, len(cfgs))
	var wg sync.WaitGroup
	for i, c := range cfgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = dial(ctx, c, onChange)
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

	r.onChange = onChange
	for _, u := range results {
		r.wg.Add(1)
		go r.supervise(u.name)
	}
}

func dial(ctx context.Context, c UpstreamConfig, onChange func(name string)) *upstream {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	u := &upstream{name: c.Name, cfg: c}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway", Version: "v0.0.1"},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
				onChange(c.Name)
			},
		})
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
	r.closeOnce.Do(func() { close(r.closed) })

	r.mu.Lock()
	var errs []error
	for _, u := range r.upstreams {
		if u.session != nil {
			if err := u.session.Close(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", u.name, err))
			}
		}
	}
	r.mu.Unlock()

	r.wg.Wait()
	return errors.Join(errs...)
}

// Refresh re-lists one upstream's tools and rebuilds the catalog.
func (r *Registry) Refresh(ctx context.Context, name string) error {
	r.mu.RLock()
	u, ok := r.upstreams[name]
	r.mu.RUnlock()

	if !ok || u.session == nil {
		return fmt.Errorf("upstream %q not connected", name)
	}

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	res, err := u.session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("refresh %q: %w", name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	u.tools = res.Tools
	r.rebuildLocked()
	log.Printf("upstream %q refreshed: %d tools", name, len(res.Tools))
	return nil
}

const (
	backoffMin = 500 * time.Millisecond
	backoffMax = 30 * time.Second
)

// supervise watches one upstream for death and redials with backoff.
func (r *Registry) supervise(name string) {
	defer r.wg.Done()

	for {
		r.mu.RLock()
		u, ok := r.upstreams[name]
		var session *mcp.ClientSession
		if ok {
			session = u.session
		}
		r.mu.RUnlock()

		if !ok {
			return
		}

		if session != nil {
			err := session.Wait()
			select {
			case <-r.closed:
				return
			default:
			}
			log.Printf("upstream %q DIED: %v", name, err)

			r.mu.Lock()
			u.session = nil
			u.tools = nil
			u.err = fmt.Errorf("died: %w", err)
			r.rebuildLocked()
			r.mu.Unlock()
		}

		if !r.redial(name) {
			return
		}
	}
}

// redial retries until success or shutdown. Reports false if shutting down.
func (r *Registry) redial(name string) bool {
	backoff := backoffMin
	for attempt := 1; ; attempt++ {
		select {
		case <-r.closed:
			return false
		case <-time.After(backoff):
		}

		r.mu.RLock()
		u, ok := r.upstreams[name]
		var cfg UpstreamConfig
		if ok {
			cfg = u.cfg
		}
		r.mu.RUnlock()
		if !ok {
			return false
		}

		fresh := dial(context.Background(), cfg, r.onChange)
		if fresh.err != nil {
			log.Printf("upstream %q redial attempt %d failed: %v", name, attempt, fresh.err)
			backoff = min(backoff*2, backoffMax)
			continue
		}

		r.mu.Lock()
		u.session = fresh.session
		u.tools = fresh.tools
		u.protocol = fresh.protocol
		u.caps = fresh.caps
		u.err = nil
		r.rebuildLocked()
		r.mu.Unlock()

		log.Printf("upstream %q RECOVERED after %d attempts: %d tools", name, attempt, len(fresh.tools))
		return true
	}
}