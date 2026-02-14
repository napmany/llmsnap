# Data Models Codemap

> Freshness: 2026-02-14 (validated)

## Configuration Schema

### Root Config (`proxy/config/config.go`, includes GroupConfig)

```yaml
healthCheckTimeout: 120        # seconds, min 15
sleepRequestTimeout: 10        # seconds, min 1
wakeRequestTimeout: 10         # seconds, min 1
logLevel: "info"               # debug | info | warn | error
logTimeFormat: "rfc3339"       # Go time format name
logToStdout: "proxy"           # proxy | upstream | both | none
metricsMaxInMemory: 1000       # max metrics in memory
captureBuffer: 5               # MB for request/response captures
startPort: 5800                # base port for auto-assignment
sendLoadingState: false        # include loading state in responses
includeAliasesInList: false    # show aliases in /v1/models
apiKeys: []                    # required API keys
macros: []                     # global macro definitions
models: {}                     # model configurations
groups: {}                     # process group configurations
hooks: {}                      # lifecycle hooks
peers: {}                      # remote peer configurations
```

### ModelConfig (`proxy/config/model_config.go`)

```yaml
models:
  "model-id":
    cmd: "server --port ${PORT}"     # required, start command
    cmdStop: "kill ${PID}"           # optional, custom stop command
    proxy: "http://localhost:${PORT}" # upstream URL (default)
    aliases: ["alias1", "alias2"]
    env: ["KEY=VALUE"]
    checkEndpoint: "/health"          # health check path (default)
    ttl: 300                          # auto-unload after N seconds idle (0=never)
    unlisted: false                   # hide from /v1/models
    useModelName: "real-name"         # override model name sent upstream
    concurrencyLimit: 100             # max concurrent requests
    name: "Display Name"
    description: "Model description"
    sendLoadingState: false
    metadata: {}                      # arbitrary key-value pairs

    # Sleep/Wake (GPU memory management)
    sleepMode: "enable"               # enable | disable
    sleepEndpoints:
      - endpoint: "/sleep?level=1"
        method: POST
        body: ""                      # optional request body
        timeout: 10                   # per-endpoint timeout
    wakeEndpoints:
      - endpoint: "/wake_up"
        method: POST

    # Request filtering (ModelFilters wraps shared Filters type)
    filters:
      stripParams: "param1,param2"    # CSV, removes from request body
      setParams:                      # overrides in request body
        key: value

    # Model-level macros (override global, ordered MacroList)
    macros:
      CUSTOM_VAR: "custom_value"      # YAML mapping, order-preserving
```

### GroupConfig (in `proxy/config/config.go`)

```yaml
groups:
  "group-name":
    swap: true          # only one member runs at a time (default: true)
    exclusive: true     # stops other groups when loading (default: true)
    persistent: false   # immune to exclusive stops (default: false)
    members:            # required, list of model IDs
      - "model-a"
      - "model-b"
```

### PeerConfig (`proxy/config/peer.go`)

```yaml
peers:
  "peer-name":
    proxy: "http://remote-host:8080"   # required, validated URL
    apiKey: "secret-key"
    models: ["remote-model-a", "remote-model-b"]  # required, non-empty
    filters:                            # shared Filters type
      stripParams: ""
      setParams: {}
```

### HooksConfig

```yaml
hooks:
  onStartup:
    preload: ["model-to-preload"]
```

### Filters (`proxy/config/filters.go`)

Shared filter type used by both `ModelConfig` (as `ModelFilters` wrapper) and `PeerConfig`:
- `StripParams` - CSV of params to remove (protected: "model")
- `SetParams` - map of params to set/override
- `SanitizedStripParams()` / `SanitizedSetParams()` - cleaned, sorted, deduplicated

## Macro System

| Macro | Scope | Description |
|---|---|---|
| `${PORT}` | cmd, proxy | Auto-assigned port (from startPort) |
| `${MODEL_ID}` | cmd | Canonical model identifier |
| `${PID}` | cmdStop only | Process ID of running server |
| `${env.VAR_NAME}` | all strings | Environment variable substitution |
| `${CUSTOM}` | cmd, proxy, metadata | User-defined (global or model-level) |

Substitution order: env vars -> global macros -> model macros (LIFO, model overrides global).

## Runtime Data Structures

### Process States

```
StateStopped ──► StateStarting ──► StateReady ──► StateStopping ──► StateStopped
                                       │                               ▲
                                       ▼                               │
                                StateSleepPending ──► StateAsleep ──► StateWaking ──► StateReady

                            (any state) ──► StateShutdown
```

### TokenMetrics

```go
type TokenMetrics struct {
    ID              int
    Timestamp       time.Time
    Model           string
    CachedTokens    int
    InputTokens     int
    OutputTokens    int
    PromptPerSecond float64
    TokensPerSecond float64
    DurationMs      int       // milliseconds
    HasCapture      bool
}
```

### ReqRespCapture

```go
type ReqRespCapture struct {
    ID          int
    ReqPath     string
    ReqHeaders  map[string]string  // single-value, sensitive headers redacted
    ReqBody     []byte
    RespHeaders map[string]string
    RespBody    []byte             // base64 encoded in JSON responses
}
```

## Event Types

| Event | ID | Fields |
|---|---|---|
| `ProcessStateChangeEvent` | 0x01 | ProcessName, NewState, OldState |
| `ChatCompletionStats` | 0x02 | TokensGenerated |
| `ConfigFileChangedEvent` | 0x03 | ReloadingState (Start/End) |
| `LogDataEvent` | 0x04 | Data []byte |
| `TokenMetricsEvent` | 0x05 | Metrics (TokenMetrics) |
| `ModelPreloadedEvent` | 0x06 | ModelName, Success |

## SSE Event Stream (`/api/events`)

Frontend subscribes to real-time updates:

```
event: modelStatus
data: {"processName":"model-id","newState":"ready","oldState":"starting"}

event: logData
data: {"data":"base64-encoded-log-line"}

event: metrics
data: {"id":1,"model":"model-id","outputTokens":42,...}
```

## JSON Schema

Configuration validated against `config-schema.json` at project root.
