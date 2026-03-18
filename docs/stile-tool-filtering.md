# Stile — Intelligent Tool Filtering

**Version:** 0.1 · **Status:** Proposal · **Date:** March 2026
*Companion to: Stile Design Document*

---

## 1. The Problem: Tool Sprawl Degrades Agent Performance

Every MCP tool exposed to an agent consumes context window tokens. A tool's name, description, and full JSON Schema input definition typically costs 200–500 tokens. An organization with 50 tools across multiple upstreams burns 10,000–25,000 tokens before the agent processes its first user message.

The impact is twofold:

1. **Context budget displacement.** Tokens spent on tool schemas are tokens unavailable for conversation history, system prompts, and reasoning. In long-running agent sessions, this pressure compounds.

2. **Decision quality degradation.** Research and practical experience show that models make worse tool selection decisions as the number of available tools increases — even when the context window can technically fit them all. The model wastes reasoning cycles evaluating irrelevant options and is more likely to select the wrong tool or hallucinate tool parameters.

Because the gateway already mediates all tool interactions, it is the natural place to solve this. The agent doesn't talk to tool servers directly, so the gateway can control exactly what the agent sees and when.

---

## 2. Design Approach

The solution has three layers of increasing sophistication. Each layer is independently useful and builds on the one before it.

| Layer | Mechanism | Token Reduction | Complexity |
|-------|-----------|----------------|------------|
| 1. Static Profiles | Config-driven tool sets per caller use case | 50–80% | Config only, no code changes to proxy |
| 2. Schema Compression | Automated rewriting of tool schemas to remove redundancy | 30–50% additional | Pure function over cached schemas |
| 3. Dynamic Selection | Meta-tool for on-demand tool discovery | 80–95% at session start | New protocol concept, session state |

---

## 3. Layer 1: Static Tool Profiles

### 3.1 Concept

Instead of exposing every tool from every upstream, define named profiles that represent specific use cases. Each caller is assigned a profile, and their `tools/list` response includes only the tools in that profile.

### 3.2 Configuration

```yaml
profiles:
  code-assistant:
    description: "Tools for coding tasks"
    tools:
      - "github/create_pull_request"
      - "github/get_file_contents"
      - "github/search_code"
      - "github/list_pull_requests"
      - "linear/create_issue"
      - "linear/list_issues"
      - "linear/get_issue"
      - "db_query"

  ops-responder:
    description: "Tools for incident response"
    tools:
      - "deploy/rollback"
      - "deploy/get_status"
      - "oncall/page"
      - "oncall/acknowledge"
      - "datadog/query_metrics"
      - "datadog/get_monitors"

  researcher:
    description: "Tools for information gathering"
    tools:
      - "github/search_code"
      - "db_query"
      - "db_schema"

callers:
  - name: claude-code-angus
    api_key_env: ANGUS_KEY
    profile: code-assistant
```

### 3.3 Behavior

When a caller with a profile hits `tools/list`, the gateway intersects the profile's tool list with the caller's `allowed_tools` ACL. The result is the smallest set: only tools that are both in the profile and permitted by the ACL. Profiles are a convenience layer on top of ACLs, not a replacement.

A caller with no profile sees all tools their ACL permits (the current behavior). Profiles are opt-in.

### 3.4 Impact

A typical coding agent needs 6–12 tools. A catalog of 50 tools compressed to 8 saves roughly 8,000–20,000 tokens per session. This is the highest-leverage, lowest-risk intervention and should ship in the MVP.

---

## 4. Layer 2: Schema Compression

### 4.1 Problem

Tool descriptions and parameter schemas are written by tool server authors for human readability, not token efficiency. They are verbose, redundant, and full of examples that restate what the schema already communicates structurally.

Example of a typical verbose parameter:

```json
{
  "name": "owner",
  "type": "string",
  "description": "The owner of the repository. This is the GitHub
   username or organization name that owns the repository you want
   to access. For example, if the repository URL is
   github.com/acme/widget, the owner would be 'acme'."
}
```

That description adds zero information beyond what the parameter name and type already convey. It costs ~50 tokens.

### 4.2 Compression Rules

The gateway applies a deterministic set of transformations to cached tool schemas before serving them to agents:

**Rule 1: Strip redundant descriptions.** If a parameter's description restates its name and type without adding semantic information, remove it. Detection heuristic: tokenize the description, check if >80% of content words also appear in the parameter name or type.

**Rule 2: Truncate long descriptions.** Cap all descriptions at a configurable maximum length (default: 80 characters). Truncate at the last word boundary before the limit. Most useful information is in the first sentence.

**Rule 3: Remove inline examples.** Strip text after common example markers: `"For example"`, `"e.g."`, `"Example:"`, `"such as"`. Examples are useful for humans reading docs; models infer usage from the schema structure itself.

**Rule 4: Collapse enum descriptions.** If a parameter has an `enum` constraint, remove any description that merely lists the enum values in prose. The enum array is self-documenting.

**Rule 5: Deduplicate nested object descriptions.** For parameters with nested objects, if the parent description restates the child parameter names, remove the parent description and let the children self-describe.

### 4.3 Implementation

Schema compression is a pure function applied to the cached tool schemas. It runs once per cache refresh, not per request. The compressed schemas are stored alongside the originals so compression can be toggled per caller or per profile.

```go
type SchemaCompressor struct {
    MaxDescLen     int    // default 80
    StripExamples  bool   // default true
    StripRedundant bool   // default true
}

func (c *SchemaCompressor) Compress(tool ToolSchema) ToolSchema {
    compressed := tool.DeepCopy()
    walkParams(compressed.InputSchema, func(p *Param) {
        if c.StripRedundant && isRedundant(p) {
            p.Description = ""
        }
        if c.StripExamples {
            p.Description = stripExamples(p.Description)
        }
        if len(p.Description) > c.MaxDescLen {
            p.Description = truncateAtWord(p.Description, c.MaxDescLen)
        }
    })
    return compressed
}
```

### 4.4 Configuration

```yaml
schema_compression:
  enabled: true
  max_description_length: 80
  strip_examples: true
  strip_redundant_descriptions: true
```

Compression can be enabled globally or per-profile. A profile can override specific settings (e.g., keep longer descriptions for a profile used by less capable models).

### 4.5 Impact

In testing with real MCP tool schemas (GitHub, Linear, filesystem), compression typically reduces per-tool token cost by 30–50%. Combined with static profiles, a session that originally consumed 20,000 tokens on tool schemas might use 2,000–4,000.

---

## 5. Layer 3: Dynamic Tool Selection

### 5.1 Concept

Instead of exposing all tools upfront, the gateway exposes a small set of always-available tools plus a **meta-tool** that agents use to discover and load additional tools on demand. Tools are pulled into context when needed rather than pushed at session start.

### 5.2 The Meta-Tool: `gateway_find_tools`

When dynamic selection is enabled, the agent's `tools/list` response contains only:

- A small set of **always-on tools** (configured per profile, typically 3–5 tools the agent uses in nearly every session)
- The `gateway_find_tools` meta-tool

```json
{
  "name": "gateway_find_tools",
  "description": "Search for available tools by describing what you need to do. Returns matching tool schemas that become available for use in this session.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "query": {
        "type": "string",
        "description": "What you need to accomplish"
      },
      "category": {
        "type": "string",
        "enum": ["code", "data", "deploy", "communicate", "search"]
      }
    },
    "required": ["query"]
  }
}
```

When the agent calls `gateway_find_tools`, the gateway searches its full tool catalog and returns the top N matching tool schemas. Those tools are then available for the agent to call for the remainder of the session.

### 5.3 Search Implementation

Tool search is implemented in three phases:

#### Phase A: Keyword & Tag Matching

Each tool is indexed with tags derived from its name, description, upstream name, and optional manually-configured tags. Search tokenizes the query and scores each tool by overlap.

```go
type ToolIndex struct {
    tools []IndexedTool
}

type IndexedTool struct {
    Schema   ToolSchema
    Tags     []string  // auto-derived + manual
    Upstream *Upstream
    Category string
}

func (idx *ToolIndex) Search(query string, limit int) []ToolSchema {
    tokens := tokenize(query)
    scored := make([]scoredTool, len(idx.tools))
    for i, tool := range idx.tools {
        scored[i] = scoredTool{
            tool:  tool,
            score: computeOverlap(tokens, tool.Tags),
        }
    }
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })
    return top(scored, limit)
}
```

This is fast, zero-dependency, and handles the majority of cases where the agent's query uses the same vocabulary as the tool names. Ships in v0.2.

#### Phase B: Semantic Search via Embeddings

For queries where vocabulary doesn't match (e.g., agent says "notify the team" and the tool is `slack/post_message`), keyword matching fails. Semantic search solves this.

At startup, the gateway computes an embedding vector for each tool's description + schema. At query time, it embeds the agent's query and ranks tools by cosine similarity.

```go
type SemanticToolIndex struct {
    tools      []IndexedTool
    embeddings [][]float32  // precomputed, one per tool
    embedder   Embedder     // interface
}

func (idx *SemanticToolIndex) Search(query string, limit int) []ToolSchema {
    qvec := idx.embedder.Embed(query)
    scored := make([]scoredTool, len(idx.tools))
    for i, tool := range idx.tools {
        scored[i] = scoredTool{
            tool:  tool,
            score: cosineSimilarity(qvec, idx.embeddings[i]),
        }
    }
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })
    return top(scored, limit)
}
```

The `Embedder` interface has two planned implementations:

- **`LocalEmbedder`:** Uses a small ONNX model (e.g., all-MiniLM-L6-v2) via Go ONNX bindings. No external API calls, ~50ms per embedding.
- **`APIEmbedder`:** Calls an external embedding API at startup to precompute vectors. Better quality, adds a startup dependency.

The tool catalog is small (tens to low hundreds), so all vectors fit in memory. No vector database needed.

#### Phase C: Session-Aware Relevance Boosting

The gateway tracks tool usage within each active session. Successfully-called tools get a relevance boost in future searches. Tools returned by search but never called get a slight penalty.

```go
type Session struct {
    ID           string
    Caller       *Caller
    Profile      *Profile
    LoadedTools  map[string]ToolSchema  // currently available
    UsageScores  map[string]float64     // tool -> session score
    AlwaysOn     []string               // from profile config
}

func (s *Session) Boost(toolName string, used bool) {
    if used {
        s.UsageScores[toolName] += 0.3
    } else {
        s.UsageScores[toolName] -= 0.05
    }
}
```

The session also provides tool eviction: if the loaded tool set grows beyond a configurable maximum (default: 15), the least-recently-used tools are evicted.

---

## 6. Session Model

Dynamic tool selection requires the gateway to maintain per-session state. This is a significant architectural addition to what is otherwise a stateless proxy.

### 6.1 Session Lifecycle

1. **Creation:** A session is created on the first request from a caller that doesn't include a session ID. The gateway generates one and returns it in a response header (`X-Gateway-Session`).
2. **Association:** Subsequent requests include the session ID in a header. The gateway looks up the session and applies its loaded tools and usage scores.
3. **Expiry:** Sessions expire after a configurable idle timeout (default: 30 minutes). A background goroutine cleans them up.

### 6.2 `tools/list` Behavior with Sessions

When a session is active and dynamic selection is enabled, `tools/list` returns:

- The always-on tools from the caller's profile
- The `gateway_find_tools` meta-tool
- Any tools the session has loaded via previous `gateway_find_tools` calls

The tool list grows during a session as the agent discovers tools it needs.

### 6.3 State Storage

Sessions are stored in memory (`sync.Map` keyed by session ID). For a single-instance gateway this is sufficient. If the gateway restarts, sessions are lost and agents re-discover tools on their next request — acceptable for v0.2.

---

## 7. Protocol Interaction

The dynamic selection mechanism is invisible to the MCP protocol. `gateway_find_tools` is a regular MCP tool from the agent's perspective — no protocol extensions required.

Typical session sequence:

| Step | Agent Action | Gateway Behavior |
|------|-------------|-----------------|
| 1 | `tools/list` | Returns always-on tools + `gateway_find_tools` (5 tools) |
| 2 | `tools/call`: `gateway_find_tools({query: "create a PR"})` | Searches tool index, returns top 3 schemas, adds to session |
| 3 | `tools/list` | Returns always-on + meta-tool + 3 loaded tools (8 total) |
| 4 | `tools/call`: `github/create_pull_request({...})` | Proxies to github upstream, records usage in session |
| 5 | `tools/call`: `gateway_find_tools({query: "query database"})` | Returns 2 more tools, adds to session |
| 6 | `tools/list` | Returns always-on + meta-tool + 5 loaded tools (10 total) |

The agent starts with ~2,000 tokens of tool context. A full session might load 10–12 tools out of 50+, saving 60–80% vs. exposing everything upfront.

---

## 8. Configuration Reference

Full configuration for all three layers:

```yaml
profiles:
  code-assistant:
    description: "Tools for coding tasks"
    always_on:                        # Layer 3: always in context
      - "github/get_file_contents"
      - "github/search_code"
      - "linear/create_issue"
    tools:                            # Layer 1: full profile tool set
      - "github/*"
      - "linear/*"
      - "db_query"
    dynamic_selection:                # Layer 3: enable meta-tool
      enabled: true
      max_loaded_tools: 15
      search_results_limit: 5

schema_compression:                   # Layer 2: global defaults
  enabled: true
  max_description_length: 80
  strip_examples: true
  strip_redundant_descriptions: true

tool_index:                           # Layer 3: search config
  type: keyword                       # keyword | semantic
  semantic:
    embedder: local                   # local | api
    model: all-MiniLM-L6-v2
  custom_tags:
    "slack/post_message": ["notify", "message", "team", "channel"]
    "github/create_pull_request": ["pr", "merge", "review", "code"]

sessions:
  idle_timeout: 30m
  max_loaded_tools: 15
  cleanup_interval: 5m
```

---

## 9. Implementation Plan

This work layers on top of the core gateway phases.

| Phase | Work | Ships With | Effort |
|-------|------|-----------|--------|
| Gateway Phase 1–2 | Static profiles (Layer 1) | Core MVP | 2–3 days |
| Gateway Phase 3 | Schema compression (Layer 2) | Policy phase | 3–4 days |
| Post-MVP (v0.2) | Dynamic selection Phase A: meta-tool, keyword search, sessions | Standalone release | 1–2 weeks |
| v0.3 | Semantic search Phase B: embedder interface, ONNX, vector index | Standalone release | 1 week |
| v0.3+ | Session-aware boosting Phase C: usage tracking, LRU eviction | With semantic search | 3–4 days |

---

## 10. Key Design Decisions

### Why a meta-tool instead of a custom MCP extension?

A meta-tool is a regular MCP tool. It works with every existing MCP client (Claude Code, Cursor, custom agent loops) without any client-side changes. A protocol extension would require client cooperation, which is unrealistic for a third-party gateway.

### Why sessions in the gateway?

Agents are stateful (conversation context), but MCP is stateless (each request is independent). The session bridges this gap by tracking loaded tools. The alternative — re-discovering tools every turn — wastes a tool call per turn and adds latency.

### Why keyword search before embeddings?

Keyword/tag search is zero-dependency, fast, and handles the 80% case (agents usually describe needs using tool-name vocabulary). Embeddings add a runtime dependency and are harder to debug. Start with keywords to validate the UX before investing in search quality.

### Why not let a model decide which tools to load?

We considered sending the full catalog to a lightweight model for selection. This adds latency, cost, and an external API dependency in the request path. Embedding-based search achieves similar quality at a fraction of the cost with deterministic behavior.

---

## 11. Success Metrics

| Metric | Target | How to Measure |
|--------|--------|---------------|
| Token reduction on `tools/list` | >70% vs. unfiltered baseline | Compare token count with and without filtering |
| Tool discovery success rate | >90% of `gateway_find_tools` calls return a used tool | Track calls followed by `tools/call` for returned tool in same session |
| Search latency (keyword) | <5ms p99 | Prometheus histogram |
| Search latency (semantic) | <50ms p99 | Prometheus histogram |
| Session memory overhead | <1MB per active session | Runtime profiling |
| Agent task completion rate | No regression vs. full exposure | A/B test on agent benchmark |

---

## 12. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Agent fails to call `gateway_find_tools` when needed | Task failure | Clear meta-tool description; always-on tools cover common cases |
| Keyword search returns irrelevant results | Wasted context | Manual tags for critical tools; upgrade to semantic search |
| Session state lost on restart | Agent must re-discover | Acceptable for v0.2; sessions are cheap to rebuild |
| Schema compression removes needed info | Incorrect tool calls | Opt-in, configurable per-profile; validate against success rates |
| Always-on set is wrong for the task | Unnecessary search calls | Profile tuning via usage metrics; multiple profiles per caller |
