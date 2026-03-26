//go:build ignore

// mock-oauth-upstream.go — a mock MCP upstream that echoes the Authorization header.
//
// Responds to tools/list with a single tool: "whoami".
// On tools/call for "whoami", returns the Authorization header it received.
// This verifies that Stile's per-user token injection is working end-to-end.
//
// Usage:
//
//	go run scripts/mock-oauth-upstream.go -port 9101
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

func main() {
	port := flag.Int("port", 9101, "listen port")
	flag.Parse()

	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		defer r.Body.Close()

		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			ID      any             `json:"id"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", 400)
			return
		}

		authHeader := r.Header.Get("Authorization")
		log.Printf("upstream received: method=%s id=%v auth=%q", msg.Method, msg.ID, authHeader)

		w.Header().Set("Content-Type", "application/json")

		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"mock-oauth-upstream","version":"0.1.0"}},"id":%s}`, marshal(msg.ID))

		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"tools":[{"name":"whoami","description":"Returns the Authorization header received by the upstream","inputSchema":{"type":"object","properties":{}}}]},"id":%s}`, marshal(msg.ID))

		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			json.Unmarshal(msg.Params, &params)

			if params.Name == "whoami" {
				text := authHeader
				if text == "" {
					text = "(no Authorization header)"
				}
				// Escape the text for JSON.
				textJSON, _ := json.Marshal(text)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":%s}]},"id":%s}`, string(textJSON), marshal(msg.ID))
			} else {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"unknown tool: %s"},"id":%s}`, params.Name, marshal(msg.ID))
			}

		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":%s}`, marshal(msg.ID))
		}
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Mock OAuth upstream (token-echo) listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
