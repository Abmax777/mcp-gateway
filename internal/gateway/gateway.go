package gateway

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Abmax777/mcp-gateway/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Middleware(reg *registry.Registry) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: reg.Catalog()}, nil

			case "tools/call":
				call, ok := req.(*mcp.CallToolRequest)
				if !ok {
					return nil, fmt.Errorf("tools/call: unexpected request type %T", req)
				}
				route, ok := reg.Lookup(call.Params.Name)
				if !ok {
					return &mcp.CallToolResult{
						IsError: true,
						Content: []mcp.Content{&mcp.TextContent{
							Text: fmt.Sprintf("unknown tool %q", call.Params.Name),
						}},
					}, nil
				}
				return route.Session.CallTool(ctx, &mcp.CallToolParams{
					Name:      route.RemoteName,
					Arguments: json.RawMessage(call.Params.Arguments),
					Meta:      call.Params.Meta,
				})
			}
			return next(ctx, method, req)
		}
	}
}