package main

import (
	"context"
	//"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Abmax777/mcp-gateway/internal/gateway"
	"github.com/Abmax777/mcp-gateway/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Upstreams []registry.UpstreamConfig `yaml:"upstreams"`
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
		case strings.Contains(u.Name, registry.Separator):
			return nil, fmt.Errorf("upstream %q contains separator %q", u.Name, registry.Separator)
		case seen[u.Name]:
			return nil, fmt.Errorf("duplicate upstream name %q", u.Name)
		}
		seen[u.Name] = true
	}
	return &c, nil
}

func main() {
	log.SetOutput(os.Stderr)
	ctx := context.Background()

	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	reg := registry.New()
	defer reg.Close()
	reg.Connect(ctx, cfg.Upstreams)

	// if err := reg.Refresh(ctx, "fs"); err != nil {
	// 	log.Printf("manual refresh: %v", err)
	// }
	// if err := reg.Refresh(ctx, "broken"); err != nil {
	// 	log.Printf("manual refresh: %v", err)
	// }

	gw := mcp.NewServer(&mcp.Implementation{
		Name:    "mcp-gateway",
		Version: "v0.0.1",
	}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		},
	})

	gw.AddReceivingMiddleware(gateway.Middleware(reg))

	log.Printf("gateway ready: %d tools", len(reg.Catalog()))

	if err := gw.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("serve: %v", err)
	}

	// catalog := reg.Catalog()
	// for _, t := range catalog {
	// 	name := t.Name
	// 	gw.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// 		route, ok := reg.Lookup(name)
	// 		if !ok {
	// 			return nil, fmt.Errorf("no route for tool %q", name)
	// 		}
	// 		return route.Session.CallTool(ctx, &mcp.CallToolParams{
	// 			Name:      route.RemoteName,
	// 			Arguments: json.RawMessage(req.Params.Arguments),
	// 			Meta:      req.Params.Meta,
	// 		})
	// 	})
	// }

	// log.Printf("gateway ready: %d tools", len(catalog))

	// if err := gw.Run(ctx, &mcp.StdioTransport{}); err != nil {
	// 	log.Fatalf("serve: %v", err)
	// }
}