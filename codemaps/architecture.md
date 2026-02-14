# Architecture Codemap

> Freshness: 2026-02-14

## Overview

llmsnap is an OpenAI-compatible proxy server that provides automatic model swapping for vLLM, llama.cpp, and other inference servers. It manages upstream process lifecycles, supports sleep/wake for GPU memory management, and provides a web UI for monitoring.

## System Diagram

```
                          ┌──────────────────┐
                          │   HTTP Clients    │
                          │  (OpenAI SDK,     │
                          │   curl, apps)     │
                          └────────┬─────────┘
                                   │
                          ┌────────▼─────────┐
                          │   ProxyManager    │
                          │  (gin HTTP router)│
                          │  - API key auth   │
                          │  - model routing  │
                          │  - CORS           │
                          └──┬─────┬────┬────┘
                   ┌─────────┘     │    └─────────┐
            ┌──────▼──────┐ ┌─────▼──────┐ ┌─────▼──────┐
            │ ProcessGroup│ │ProcessGroup│ │ PeerProxy  │
            │ "default"   │ │ "sleepers" │ │ (remote)   │
            │ swap=true   │ │ swap=true  │ └────────────┘
            │ exclusive   │ │ exclusive  │
            └──┬──────┬───┘ └──┬─────┬──┘
          ┌────▼──┐┌──▼───┐┌──▼───┐┌▼────┐
          │Process││Process││Process││Proc.│
          │modelA ││modelB ││modelC ││mod D│
          └───┬───┘└──┬───┘└──┬───┘└─┬───┘
              │       │       │      │
          ┌───▼───┐┌──▼──┐┌──▼──┐┌──▼──┐
          │vLLM   ││llama││vLLM ││other│
          │server ││.cpp ││     ││     │
          └───────┘└─────┘└─────┘└─────┘
```

## Package Structure

```
llmsnap/
├── llama-swap.go          # Main entry point, CLI flags, HTTP server, signal handling
├── proxy/                 # Core proxy package
│   ├── proxymanager.go    # HTTP routing, model resolution, request proxying
│   ├── proxymanager_api.go# /api/* endpoints (SSE events, metrics, captures)
│   ├── proxymanager_loghandlers.go  # Log streaming endpoints
│   ├── processgroup.go   # Process group lifecycle (swap, exclusive, persistent)
│   ├── process.go         # Upstream process management (start/stop/sleep/wake)
│   ├── process_unix.go    # Platform-specific process setup (unix)
│   ├── process_windows.go # Platform-specific process setup (windows)
│   ├── peerproxy.go       # Remote peer proxy support
│   ├── logMonitor.go      # Structured logging with circular buffer
│   ├── metrics_monitor.go # Token metrics collection & request/response capture
│   ├── events.go          # Event type definitions
│   ├── ui_embed.go        # Embedded Svelte UI assets
│   ├── ui_compress.go     # Brotli/gzip compression for UI
│   ├── sanitize_cors.go   # CORS header sanitization
│   ├── discardWriter.go   # Mock ResponseWriter for preloading
│   └── config/            # Configuration package
│       ├── config.go      # Root config, YAML loading, env substitution
│       ├── model_config.go# Per-model config (cmd, sleep, filters, macros)
│       └── groups.go      # Group config (swap, exclusive, persistent)
├── event/                 # Generic event bus
│   └── event.go           # Lock-free pub/sub dispatcher with generics
├── cmd/                   # CLI tools
│   ├── wol-proxy/         # Wake-on-LAN proxy server
│   └── simple-responder/  # Test responder for development
├── ui-svelte/             # Svelte 5 + Vite frontend (see frontend.md)
├── models/                # Model definitions/configs
├── docs/                  # Documentation
├── scripts/               # Build and utility scripts
└── docker/                # Docker configurations
```

## Key Internal Dependencies

```
main (llama-swap.go)
  ├── proxy           (ProxyManager, ProcessGroup, Process)
  ├── proxy/config    (Config, ModelConfig, GroupConfig)
  └── event           (Dispatcher, event types)

proxy
  ├── proxy/config    (configuration structs)
  ├── event           (publish/subscribe events)
  └── gin             (HTTP routing framework)

proxy/config          (no internal dependencies)
event                 (no internal dependencies)
```

## Request Flow

1. HTTP request arrives at ProxyManager (gin router)
2. API key middleware validates authentication
3. Model name extracted from JSON body `"model"` field
4. `config.RealModelName()` resolves aliases to canonical model ID
5. `swapProcessGroup()` finds or activates the correct ProcessGroup
   - If exclusive group, stops other non-persistent groups
   - If swap group, stops other processes within the group
6. `Process.start()` launches upstream server if not running
   - Executes `cmd` with macro substitution (${PORT}, ${MODEL_ID}, etc.)
   - Polls `checkEndpoint` until healthy (with configurable timeout)
7. `Process.ProxyRequest()` forwards request via `httputil.ReverseProxy`
   - Applies filters (stripParams, setParams, useModelName)
   - Tracks in-flight requests, enforces concurrency limits
8. Response streamed back to client
9. `metricsMonitor` extracts token usage from response headers/body
10. TTL timer starts; process sleeps or stops after idle timeout

## Key Patterns

- **Mutex-based group locking**: ProxyManager.Mutex guards process group operations
- **Atomic state machine**: Process uses atomic state transitions (Stopped -> Starting -> Ready -> Stopping -> Stopped)
- **Event-driven UI updates**: Process state changes emit events -> SSE stream -> UI reactivity
- **Macro system**: Three-phase config loading (env vars -> YAML -> macro substitution)
- **Embedded UI**: Svelte build output embedded in Go binary via `//go:embed`
