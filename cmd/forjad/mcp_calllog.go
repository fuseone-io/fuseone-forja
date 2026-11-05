package main

import (
	"context"
	"encoding/json"
	"log"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// logCalls logs every tool call as the client's name, the tool and the
// run_id the tool answered, "-" for what is absent. Arguments are never
// logged: they may hold free text. A tool name is logged only when the
// server ran a tool by it, since an unknown name is the caller's free text.
func logCalls(logger *log.Logger) sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			res, err := next(ctx, method, req)
			if call, ok := req.(*sdk.CallToolRequest); ok {
				logger.Printf("forjad: call client=%s tool=%s run_id=%s", clientOf(call), toolOf(call, err), runIDOf(res))
			}
			return res, err
		}
	}
}

func clientOf(call *sdk.CallToolRequest) string {
	if extra := call.GetExtra(); extra != nil && extra.TokenInfo != nil && extra.TokenInfo.UserID != "" {
		return extra.TokenInfo.UserID
	}
	return "-"
}

func toolOf(call *sdk.CallToolRequest, err error) string {
	if err != nil || call.Params == nil {
		return "-"
	}
	return call.Params.Name
}

// runIDOf is the run_id field of a tool's structured result.
func runIDOf(res sdk.Result) string {
	tool, ok := res.(*sdk.CallToolResult)
	if !ok || tool == nil || tool.StructuredContent == nil {
		return "-"
	}
	raw, err := json.Marshal(tool.StructuredContent)
	if err != nil {
		return "-"
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(raw, &out) != nil || out.RunID == "" {
		return "-"
	}
	return out.RunID
}
