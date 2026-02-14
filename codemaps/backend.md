# Backend Codemap

> Freshness: 2026-02-14

## Entry Point

**`llama-swap.go`** - Main application
- Parses CLI flags: `--config`, `--listen`, `--tls-cert`, `--tls-key`, `--watch-config`, `--version`
- Loads config via `config.LoadConfig()`
- Creates `ProxyManager` and starts HTTP server
- Optional config file watcher (fsnotify) for hot-reload
- Graceful shutdown on SIGINT/SIGTERM

## Core Types

### ProxyManager (`proxy/proxymanager.go`)
Central orchestrator implementing `http.Handler`.
- **Fields**: config, ginEngine, loggers (proxy/upstream/mux), metricsMonitor, processGroups map, peerProxy, shutdown context
- **Key methods**: `setupGinEngine()`, `swapProcessGroup()`, `proxyInferenceHandler()`, `listModelsHandler()`, `apiKeyAuth()`

### ProcessGroup (`proxy/processgroup.go`)
Manages a group of related model processes.
- **Fields**: id, swap, exclusive, persistent, processes map, lastUsedProcess
- **Key methods**: `ProxyRequest()`, `HasMember()`, `StopProcess()`, `SleepProcess()`, `Shutdown()`

### Process (`proxy/process.go`)
Manages a single upstream inference server.
- **States**: Stopped, Starting, Ready, Stopping, Shutdown, SleepPending, Asleep, Waking
- **Fields**: ID, config, cmd, reverseProxy, state (atomic), inFlightRequests (WaitGroup), concurrencySemaphore
- **Key methods**: `start()`, `Stop()`, `StopImmediately()`, `Sleep()`, `Wake()`, `ProxyRequest()`, `checkHealthEndpoint()`, `startUnloadMonitoring()`

### PeerProxy (`proxy/peerproxy.go`)
Routes requests to remote llmsnap peers.
- **Fields**: peers config, proxyMap (modelID -> peerProxyMember)
- **Key methods**: `HasPeerModel()`, `GetPeerFilters()`, `ProxyRequest()`

### LogMonitor (`proxy/logMonitor.go`)
Structured logger with circular buffer and event emission.
- **Fields**: eventbus, buffer (lazy circular), stdout writer, level, prefix
- **Key methods**: `Write()`, `GetHistory()`, `Debug/Info/Warn/Error()`, `OnLogData()`

### MetricsMonitor (`proxy/metrics_monitor.go`)
Collects token metrics and captures request/response pairs.
- **Fields**: metrics list, captures map, FIFO eviction
- **Key methods**: `addMetrics()`, `wrapHandler()`, `getCapture()`

## HTTP Routes

### Inference (POST, API key required)
| Route | Handler |
|---|---|
| `/v1/chat/completions` | `proxyInferenceHandler` |
| `/v1/completions` | `proxyInferenceHandler` |
| `/v1/responses` | `proxyInferenceHandler` |
| `/v1/messages` | `proxyInferenceHandler` |
| `/v1/messages/count_tokens` | `proxyInferenceHandler` |
| `/v1/embeddings` | `proxyInferenceHandler` |
| `/reranking`, `/rerank`, `/v1/rerank` | `proxyInferenceHandler` |
| `/infill`, `/completion` | `proxyInferenceHandler` |
| `/v1/audio/speech`, `/v1/audio/voices` | `proxyInferenceHandler` |
| `/v1/audio/transcriptions` | `proxyOAIPostFormHandler` |
| `/v1/images/generations` | `proxyInferenceHandler` |
| `/v1/images/edits` | `proxyOAIPostFormHandler` |

### Model Management
| Route | Method | Handler |
|---|---|---|
| `/v1/models` | GET | `listModelsHandler` |
| `/running` | GET | `listRunningProcessesHandler` |
| `/unload` | GET | `unloadAllModelsHandler` |
| `/api/models/unload` | POST | Unload all |
| `/api/models/unload/:model` | POST | Unload single |
| `/api/models/sleep/:model` | POST | Sleep single |

### Monitoring & UI
| Route | Method | Purpose |
|---|---|---|
| `/api/events` | GET | SSE event stream |
| `/api/metrics` | GET | Token metrics |
| `/api/captures/:id` | GET | Request/response capture |
| `/api/version` | GET | Version info |
| `/logs` | GET | Log history |
| `/logs/stream` | GET | Log SSE stream |
| `/health` | GET | Health check |
| `/upstream/:model/*path` | ANY | Direct upstream proxy |
| `/ui/*` | GET | Embedded Svelte SPA |

## Concurrency Patterns

- `sync.Mutex` on ProxyManager for group operations
- `sync.RWMutex` on Process for state reads vs writes
- `atomic.Int32` for process state transitions (`swapState`)
- `sync.WaitGroup` for in-flight request tracking
- Channel-based semaphore for per-model concurrency limits
- `sync.RWMutex` on circular buffer for log reads/writes

## File Listing

| File | Lines | Purpose |
|---|---|---|
| `proxy/proxymanager.go` | ~850 | Core proxy routing and model resolution |
| `proxy/proxymanager_api.go` | ~200 | API endpoints (events, metrics, captures) |
| `proxy/proxymanager_loghandlers.go` | ~70 | Log streaming handlers |
| `proxy/process.go` | ~900 | Upstream process lifecycle |
| `proxy/processgroup.go` | ~120 | Process group management |
| `proxy/peerproxy.go` | ~120 | Remote peer proxy |
| `proxy/logMonitor.go` | ~180 | Structured logging |
| `proxy/metrics_monitor.go` | ~450 | Metrics and capture |
| `proxy/events.go` | ~60 | Event type definitions |
| `proxy/config/config.go` | ~500 | Root config and loading |
| `proxy/config/model_config.go` | ~300 | Model config structs |
| `proxy/config/groups.go` | ~80 | Group config |
| `event/event.go` | ~300 | Generic event dispatcher |
