// mock_stdio_server is a simple MCP server that communicates via stdin/stdout
// using line-delimited JSON-RPC. It is used for testing the StdioTransport.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id"`
}

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolSchema struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"inputSchema,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			resp := response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error"},
				ID:      nil,
			}
			encoder.Encode(resp)
			continue
		}

		var resp response
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "initialize":
			resp.Result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]string{
					"name":    "mock-stdio-server",
					"version": "1.0.0",
				},
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
			}

		case "tools/list":
			resp.Result = map[string]interface{}{
				"tools": []toolSchema{
					{
						Name:        "test_echo",
						Description: "Echoes back the input",
						InputSchema: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"message": map[string]string{"type": "string"},
							},
						},
					},
				},
			}

		case "tools/call":
			var params struct {
				Arguments map[string]interface{} `json:"arguments"`
			}
			if req.Params != nil {
				json.Unmarshal(req.Params, &params)
			}
			msg := ""
			if m, ok := params.Arguments["message"]; ok {
				msg = fmt.Sprintf("%v", m)
			}
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": msg,
					},
				},
			}

		case "ping":
			resp.Result = map[string]interface{}{}

		default:
			resp.Error = &rpcError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			}
		}

		encoder.Encode(resp)
	}
}
