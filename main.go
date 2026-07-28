package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	ctx := context.Background()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-gateway",
		Version: "v0.0.1",
	}, nil)

	cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp/gwtest")
	cmd.Stderr = os.Stderr

	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		log.Fatalf("list tools: %v", err)
	}

	// for _, t := range res.Tools {
	// 	log.Printf("%-28s readOnly=%v", t.Name, t.Annotations != nil && t.Annotations.ReadOnlyHint)
	// }

	gw := mcp.NewServer(&mcp.Implementation{
		Name:    "mcp-gateway",
		Version: "v0.0.1",
	}, nil)

	for _, t := range res.Tools {
		local := *t
		remoteName := t.Name
		local.Name = "fs__" + t.Name

		gw.AddTool(&local, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return session.CallTool(ctx, &mcp.CallToolParams{
				Name:      remoteName,
				Arguments: json.RawMessage(req.Params.Arguments),
				Meta:      req.Params.Meta,
			})
		})
	}

	log.Printf("gateway ready: %d tools", len(res.Tools))

	if err := gw.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("serve: %v", err)
	}
}