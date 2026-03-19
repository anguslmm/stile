// fake-upstream.go — a minimal MCP upstream server for testing Stile.
// Responds to tools/list and tools/call with canned responses.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := ":9090"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

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

		log.Printf("upstream received: method=%s id=%v", msg.Method, msg.ID)

		w.Header().Set("Content-Type", "application/json")

		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake-upstream","version":"0.1.0"}},"id":%s}`, marshal(msg.ID))

		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"tools":[{"name":"echo","description":"Echoes back the input","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}},{"name":"add","description":"Adds two numbers","inputSchema":{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}}]},"id":%s}`, marshal(msg.ID))

		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(msg.Params, &params)

			switch params.Name {
			case "echo":
				var args struct{ Message string `json:"message"` }
				json.Unmarshal(params.Arguments, &args)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"Echo: %s"}]},"id":%s}`, args.Message, marshal(msg.ID))
			case "add":
				var args struct {
					A float64 `json:"a"`
					B float64 `json:"b"`
				}
				json.Unmarshal(params.Arguments, &args)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"Result: %g"}]},"id":%s}`, args.A+args.B, marshal(msg.ID))
			default:
				fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"unknown tool: %s"},"id":%s}`, params.Name, marshal(msg.ID))
			}

		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":%s}`, marshal(msg.ID))
		}
	})

	log.Printf("Fake MCP upstream listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
