package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

const separator = "__"

type Config struct {
	Upstreams []UpstreamConfig `yaml:"upstreams"`
}

type UpstreamConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type upstream struct {
	cfg     UpstreamConfig
	session *mcp.ClientSession
	tools   []*mcp.Tool
	err     error
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, u := range c.Upstreams {
		switch {
		case u.Name == "":
			return nil, fmt.Errorf("upstream with empty name")
		case strings.Contains(u.Name, separator):
			return nil, fmt.Errorf("upstream %q contains separator %q", u.Name, separator)
		case seen[u.Name]:
			return nil, fmt.Errorf("duplicate upstream name %q", u.Name)
		}
		seen[u.Name] = true
	}
	return &c, nil
}

func connect(ctx context.Context, uc UpstreamConfig) upstream {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := upstream{cfg: uc}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway", Version: "v0.0.1"}, nil)
	cmd := exec.Command(uc.Command, uc.Args...)
	cmd.Stderr = os.Stderr

	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		u.err = fmt.Errorf("connect: %w", err)
		return u
	}
	u.session = session

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		u.err = fmt.Errorf("list tools: %w", err)
		return u
	}
	u.tools = res.Tools
	return u
}

func main() {
	log.SetOutput(os.Stderr)
	ctx := context.Background()

	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ups := make([]upstream, len(cfg.Upstreams))
	var wg sync.WaitGroup
	for i, uc := range cfg.Upstreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ups[i] = connect(ctx, uc)
		}()
	}
	wg.Wait()

	gw := mcp.NewServer(&mcp.Implementation{Name: "mcp-gateway", Version: "v0.0.1"}, nil)

	total, live := 0, 0
	for i := range ups {
		u := &ups[i]
		if u.err != nil {
			log.Printf("upstream %q DEGRADED: %v", u.cfg.Name, u.err)
			continue
		}
		defer u.session.Close()
		live++

		session := u.session
		for _, t := range u.tools {
			local := *t
			remoteName := t.Name
			local.Name = u.cfg.Name + separator + t.Name

			gw.AddTool(&local, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return session.CallTool(ctx, &mcp.CallToolParams{
					Name:      remoteName,
					Arguments: json.RawMessage(req.Params.Arguments),
					Meta:      req.Params.Meta,
				})
			})
			total++
		}
		log.Printf("upstream %q: %d tools", u.cfg.Name, len(u.tools))
	}

	log.Printf("gateway ready: %d tools from %d/%d upstreams", total, live, len(ups))

	if err := gw.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("serve: %v", err)
	}
}