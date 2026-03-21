//go:build ignore

// mock-sse-server is a minimal MCP server that responds to tools/call
// with a slow SSE stream of lorem ipsum text. Used to verify SSE passthrough.
//
// Usage: go run scripts/mock-sse-server.go
// Listens on :9090
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var loremSentences = []string{
	"Lorem ipsum dolor sit amet, consectetur adipiscing elit.",
	"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.",
	"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris.",
	"Duis aute irure dolor in reprehenderit in voluptate velit esse cillum.",
	"Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia.",
	"Nemo enim ipsam voluptatem quia voluptas sit aspernatur aut odit aut fugit.",
	"Neque porro quisquam est, qui dolorem ipsum quia dolor sit amet.",
	"Ut enim ad minima veniam, quis nostrum exercitationem ullam corporis.",
}

func main() {
	http.HandleFunc("/", handleMCP)
	fmt.Println("Mock SSE MCP server listening on :9090")
	http.ListenAndServe(":9090", nil)
}

func handleMCP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		ID      any             `json:"id"`
	}
	json.Unmarshal(body, &req)

	switch req.Method {
	case "initialize":
		writeJSON(w, req.ID, map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mock-sse", "version": "0.1.0"},
		})

	case "tools/list":
		writeJSON(w, req.ID, map[string]any{
			"tools": []map[string]any{
				{
					"name":        "lorem_stream",
					"description": "Streams lorem ipsum text slowly via SSE",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
			},
		})

	case "tools/call":
		streamSSE(w, req.ID)

	default:
		writeJSON(w, req.ID, map[string]any{})
	}
}

func streamSSE(w http.ResponseWriter, id any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	// Stream progress notifications with lorem ipsum, one sentence at a time
	var accumulated []string
	for i, sentence := range loremSentences {
		accumulated = append(accumulated, sentence)

		progress := map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params": map[string]any{
				"progress": i + 1,
				"total":    len(loremSentences),
				"message":  sentence,
			},
		}
		data, _ := json.Marshal(progress)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
		time.Sleep(300 * time.Millisecond)
	}

	// Final response with the complete text
	fullText := strings.Join(accumulated, " ")
	final := map[string]any{
		"jsonrpc": "2.0",
		"result": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fullText},
			},
		},
		"id": id,
	}
	data, _ := json.Marshal(final)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, id any, result any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"result":  result,
		"id":      id,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
