# Migrate Sleep/Wake from Commands to HTTP Endpoints

## Title
Migrate from command-based to HTTP endpoint-based sleep/wake functionality

## Overview

The current implementation of sleep/wake functionality uses shell command execution (`cmdSleep` and `cmdWake`) with configurations like:

```yaml
cmdSleep: curl -X POST http://localhost:${PORT}/sleep -d '{"level": "1"}'
cmdWake: curl -X POST http://localhost:${PORT}/wake_up
```

This approach has several drawbacks:
- **Security**: Shell command execution introduces potential command injection risks
- **Complexity**: Requires users to construct curl commands with proper escaping
- **Overhead**: Spawning shell processes adds latency
- **Error handling**: Parsing command exit codes is less reliable than HTTP status codes
- **Inconsistency**: Health checks use HTTP directly, but sleep/wake use shell commands

### Proposed Solution

Migrate to direct HTTP endpoint configuration, similar to how `checkEndpoint` works:

```yaml
sleepEndpoint: /sleep
sleepMethod: POST
sleepBody: '{"level": "1"}'
wakeEndpoint: /wake_up
wakeMethod: POST
```

This approach:
- **Eliminates shell execution**: Direct HTTP requests are faster and more secure
- **Simplifies configuration**: No need to construct curl commands
- **Improves consistency**: All HTTP operations (health, sleep, wake) use the same pattern
- **Better error handling**: HTTP status codes provide clear success/failure indication
- **Reduces latency**: No shell process spawning overhead

### Backward Compatibility not needed

Old (`cmdSleep`/`cmdWake`) methods should be deleted


## Design Requirements

### 1. Configuration Changes

**New fields in `ModelConfig` (proxy/config/model_config.go)**:

```go
type ModelConfig struct {
    // ... existing fields ...

    // delete
    CmdSleep         string   `yaml:"cmdSleep"`
    // delete
    CmdWake          string   `yaml:"cmdWake"`

    // New HTTP-based sleep/wake configuration
    SleepEndpoint    string   `yaml:"sleepEndpoint"`
    SleepMethod      string   `yaml:"sleepMethod"`      // Default: "POST"
    SleepBody        string   `yaml:"sleepBody"`        // Optional JSON body
    SleepTimeout     int      `yaml:"sleepTimeout"`     // Seconds, uses global if 0

    WakeEndpoint     string   `yaml:"wakeEndpoint"`
    WakeMethod       string   `yaml:"wakeMethod"`       // Default: "POST"
    WakeBody         string   `yaml:"wakeBody"`         // Optional JSON body
    WakeTimeout      int      `yaml:"wakeTimeout"`      // Seconds, uses global if 0

    // delete
    CmdSleepTimeout  int      `yaml:"cmdSleepTimeout"`
    CmdWakeTimeout   int      `yaml:"cmdWakeTimeout"`
}
```

**Configuration validation**:
- If `sleepEndpoint` is defined, `wakeEndpoint` must also be defined
- If `wakeEndpoint` is defined, `sleepEndpoint` must also be defined
- Method must be one of: GET, POST, PUT, PATCH (default: POST)
- Endpoint should be a path (e.g., `/sleep`) not a full URL (uses `Proxy` as base)

**Example configurations**:

```yaml
# vLLM with level 1 sleep (offload to CPU RAM)
models:
  "vllm-model":
    cmd: docker run --rm --name ${MODEL_ID} vllm/vllm-openai --model /models/my-model
    proxy: http://localhost:${PORT}
    env:
      - "VLLM_SERVER_DEV_MODE=1"

    sleepEndpoint: /sleep
    sleepMethod: POST
    sleepBody: '{"level": "1"}'

    wakeEndpoint: /wake_up
    wakeMethod: POST

# vLLM with level 2 sleep (discard weights)
models:
  "vllm-model-aggressive":
    # ... similar config ...
    sleepBody: '{"level": "2"}'

# Simple sleep without body
models:
  "custom-server":
    sleepEndpoint: /api/sleep
    wakeEndpoint: /api/wake
    # sleepBody omitted - no body sent

# Custom methods and timeouts
models:
  "another-server":
    sleepEndpoint: /v1/model/sleep
    sleepMethod: PUT
    sleepTimeout: 120  # 2 minutes

    wakeEndpoint: /v1/model/wake
    wakeMethod: PUT
    wakeTimeout: 90    # 1.5 minutes
```

### 2. Process Implementation Changes

**File: `proxy/process.go`**

#### 2.1 New HTTP Helper Methods

Add methods similar to `checkHealthEndpoint()` but for sleep/wake:

```go
// sendSleepRequest sends HTTP request to sleep endpoint
func (p *Process) sendSleepRequest() error {
    if p.config.SleepEndpoint == "" {
        return fmt.Errorf("sleepEndpoint not configured")
    }

    // Build full URL from proxy base + endpoint
    sleepURL, err := url.JoinPath(p.config.Proxy, p.config.SleepEndpoint)
    if err != nil {
        return fmt.Errorf("failed to create sleep URL: %v", err)
    }

    // Determine method (default POST)
    method := p.config.SleepMethod
    if method == "" {
        method = "POST"
    }

    // Get timeout (model-specific or global)
    timeout := p.getSleepTimeout()

    // Create HTTP client with timeout
    client := &http.Client{
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout: 2 * time.Second,  // Connection timeout
            }).DialContext,
        },
        Timeout: timeout,  // Overall request timeout
    }

    // Prepare request body if configured
    var bodyReader io.Reader
    if p.config.SleepBody != "" {
        bodyReader = strings.NewReader(p.config.SleepBody)
    }

    // Create and execute request
    req, err := http.NewRequest(method, sleepURL, bodyReader)
    if err != nil {
        return fmt.Errorf("failed to create sleep request: %v", err)
    }

    if p.config.SleepBody != "" {
        req.Header.Set("Content-Type", "application/json")
    }

    p.proxyLogger.Debugf("<%s> Sending sleep request: %s %s", p.ID, method, sleepURL)

    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("sleep request failed: %v", err)
    }
    defer resp.Body.Close()

    // Check response status
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("sleep request returned status %d: %s", resp.StatusCode, string(body))
    }

    p.proxyLogger.Debugf("<%s> Sleep request successful (status %d)", p.ID, resp.StatusCode)
    return nil
}

// sendWakeRequest sends HTTP request to wake endpoint
func (p *Process) sendWakeRequest() error {
    if p.config.WakeEndpoint == "" {
        return fmt.Errorf("wakeEndpoint not configured")
    }

    // Build full URL from proxy base + endpoint
    wakeURL, err := url.JoinPath(p.config.Proxy, p.config.WakeEndpoint)
    if err != nil {
        return fmt.Errorf("failed to create wake URL: %v", err)
    }

    // Determine method (default POST)
    method := p.config.WakeMethod
    if method == "" {
        method = "POST"
    }

    // Get timeout (model-specific or global)
    timeout := p.getWakeTimeout()

    // Create HTTP client with timeout
    client := &http.Client{
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout: 2 * time.Second,  // Connection timeout
            }).DialContext,
        },
        Timeout: timeout,  // Overall request timeout
    }

    // Prepare request body if configured
    var bodyReader io.Reader
    if p.config.WakeBody != "" {
        bodyReader = strings.NewReader(p.config.WakeBody)
    }

    // Create and execute request
    req, err := http.NewRequest(method, wakeURL, bodyReader)
    if err != nil {
        return fmt.Errorf("failed to create wake request: %v", err)
    }

    if p.config.WakeBody != "" {
        req.Header.Set("Content-Type", "application/json")
    }

    p.proxyLogger.Debugf("<%s> Sending wake request: %s %s", p.ID, method, wakeURL)

    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("wake request failed: %v", err)
    }
    defer resp.Body.Close()

    // Check response status
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("wake request returned status %d: %s", resp.StatusCode, string(body))
    }

    p.proxyLogger.Debugf("<%s> Wake request successful (status %d)", p.ID, resp.StatusCode)
    return nil
}
```

#### 2.2 Replace `executeSleepCommand()` Method

Replace the existing method to only use HTTP endpoints:

```go
// executeSleepCommand executes sleep operation via HTTP endpoint
func (p *Process) executeSleepCommand() error {
    if p.cmd == nil || p.cmd.Process == nil {
        return fmt.Errorf("process is nil, cannot execute sleep")
    }

    if p.config.SleepEndpoint == "" {
        return fmt.Errorf("sleepEndpoint not configured")
    }

    sleepTimeout := p.getSleepTimeout()
    p.proxyLogger.Infof("<%s> Executing HTTP sleep request (timeout: %v)", p.ID, sleepTimeout)

    if err := p.sendSleepRequest(); err != nil {
        return fmt.Errorf("HTTP sleep request failed: %v", err)
    }

    return nil
}
```

#### 2.3 Replace `cmdWakeUpstreamProcess()` Method

Replace the existing method to only use HTTP endpoints:

```go
// cmdWakeUpstreamProcess wakes the upstream process via HTTP endpoint
func (p *Process) cmdWakeUpstreamProcess() error {
    p.processLogger.Debugf("<%s> cmdWakeUpstreamProcess() initiating wake", p.ID)

    if p.cmd == nil || p.cmd.Process == nil {
        return fmt.Errorf("process is nil, cannot execute wake")
    }

    if p.config.WakeEndpoint == "" {
        return fmt.Errorf("wakeEndpoint not configured")
    }

    wakeTimeout := p.getWakeTimeout()
    p.proxyLogger.Infof("<%s> Executing HTTP wake request (timeout: %v)", p.ID, wakeTimeout)

    if err := p.sendWakeRequest(); err != nil {
        return fmt.Errorf("HTTP wake request failed: %v", err)
    }

    return nil
}
```

#### 2.4 Update `getSleepTimeout()` and `getWakeTimeout()`

Simplify these methods to only check new config fields:

```go
// getSleepTimeout returns the sleep timeout from config or defaults
func (p *Process) getSleepTimeout() time.Duration {
    // Model-specific timeout
    if p.config.SleepTimeout > 0 {
        return time.Duration(p.config.SleepTimeout) * time.Second
    }
    // Global config
    if p.globalSleepTimeout > 0 {
        return time.Duration(p.globalSleepTimeout) * time.Second
    }
    // Default fallback
    return 60 * time.Second
}

// getWakeTimeout returns the wake timeout from config or defaults
func (p *Process) getWakeTimeout() time.Duration {
    // Model-specific timeout
    if p.config.WakeTimeout > 0 {
        return time.Duration(p.config.WakeTimeout) * time.Second
    }
    // Global config
    if p.globalWakeTimeout > 0 {
        return time.Duration(p.globalWakeTimeout) * time.Second
    }
    // Default fallback
    return 60 * time.Second
}
```

#### 2.5 Update Sleep Detection Logic

Simplify the sleep mode detection:

```go
// isSleepEnabled returns true if sleep/wake endpoints are configured
func (p *Process) isSleepEnabled() bool {
    return p.config.SleepEndpoint != "" && p.config.WakeEndpoint != ""
}
```

Use this in `Sleep()` method:

```go
func (p *Process) Sleep() {
    if !p.isSleepEnabled() {
        p.proxyLogger.Errorf("<%s> sleep not configured", p.ID)
        return
    }

    // ... rest of Sleep() logic ...
}
```

### 3. Configuration Validation

**File: `proxy/config/model_config.go`**

Add validation in `UnmarshalYAML`:

```go
func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
    type rawModelConfig ModelConfig
    defaults := rawModelConfig{
        // ... existing defaults ...
        SleepMethod:      "POST",  // Default HTTP method
        WakeMethod:       "POST",
        SleepTimeout:     0,       // Use global if 0
        WakeTimeout:      0,
    }

    if err := unmarshal(&defaults); err != nil {
        return err
    }

    *m = ModelConfig(defaults)

    // Validation: if one endpoint is set, both must be set
    if (m.SleepEndpoint != "" && m.WakeEndpoint == "") {
        return fmt.Errorf("wakeEndpoint required when sleepEndpoint is configured")
    }
    if (m.WakeEndpoint != "" && m.SleepEndpoint == "") {
        return fmt.Errorf("sleepEndpoint required when wakeEndpoint is configured")
    }

    // Validate HTTP methods
    validMethods := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true}
    if m.SleepMethod != "" && !validMethods[strings.ToUpper(m.SleepMethod)] {
        return fmt.Errorf("invalid sleepMethod: %s (must be GET, POST, PUT, or PATCH)", m.SleepMethod)
    }
    if m.WakeMethod != "" && !validMethods[strings.ToUpper(m.WakeMethod)] {
        return fmt.Errorf("invalid wakeMethod: %s (must be GET, POST, PUT, or PATCH)", m.WakeMethod)
    }

    // Normalize methods to uppercase
    if m.SleepMethod != "" {
        m.SleepMethod = strings.ToUpper(m.SleepMethod)
    }
    if m.WakeMethod != "" {
        m.WakeMethod = strings.ToUpper(m.WakeMethod)
    }

    return nil
}
```

### 4. Documentation Updates

**Files to update**:
- Configuration documentation with HTTP endpoint examples
- Update all example configurations
- Add CHANGELOG entry for breaking change
- Update README with new sleep/wake syntax

**Configuration documentation example**:

```markdown
## Sleep/Wake Configuration

Sleep/wake mode allows models to be put into a low-resource state instead of terminating them entirely.

### Configuration

```yaml
models:
  "vllm-model":
    sleepEndpoint: /sleep
    sleepMethod: POST              # Optional, default: POST
    sleepBody: '{"level": "1"}'    # Optional JSON body
    sleepTimeout: 60               # Optional, seconds

    wakeEndpoint: /wake_up
    wakeMethod: POST               # Optional, default: POST
    wakeBody: '{}'                 # Optional JSON body
    wakeTimeout: 90                # Optional, seconds
```

### Benefits
- Fast model switching (sub-second wake times)
- Reduced memory footprint while sleeping
- No process termination/restart overhead
- HTTP-based (secure, no shell commands)
```

## Testing Plan

### Unit Tests

**File: `proxy/config/model_config_test.go`**

```go
func TestModelConfig_SleepWakeEndpointValidation(t *testing.T) {
    tests := []struct {
        name        string
        config      string
        expectError bool
        errorMsg    string
    }{
        {
            name: "both endpoints configured",
            config: `
sleepEndpoint: /sleep
wakeEndpoint: /wake
`,
            expectError: false,
        },
        {
            name: "only sleep endpoint",
            config: `
sleepEndpoint: /sleep
`,
            expectError: true,
            errorMsg:    "wakeEndpoint required",
        },
        {
            name: "only wake endpoint",
            config: `
wakeEndpoint: /wake
`,
            expectError: true,
            errorMsg:    "sleepEndpoint required",
        },
        {
            name: "invalid sleep method",
            config: `
sleepEndpoint: /sleep
wakeEndpoint: /wake
sleepMethod: DELETE
`,
            expectError: true,
            errorMsg:    "invalid sleepMethod",
        },
        {
            name: "valid custom methods",
            config: `
sleepEndpoint: /sleep
wakeEndpoint: /wake
sleepMethod: PUT
wakeMethod: PATCH
`,
            expectError: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var mc ModelConfig
            err := yaml.Unmarshal([]byte(tt.config), &mc)

            if tt.expectError {
                assert.Error(t, err)
                if tt.errorMsg != "" {
                    assert.Contains(t, err.Error(), tt.errorMsg)
                }
            } else {
                assert.NoError(t, err)
            }
        })
    }
}

func TestModelConfig_MethodNormalization(t *testing.T) {
    config := `
sleepEndpoint: /sleep
wakeEndpoint: /wake
sleepMethod: post
wakeMethod: put
`
    var mc ModelConfig
    err := yaml.Unmarshal([]byte(config), &mc)
    assert.NoError(t, err)
    assert.Equal(t, "POST", mc.SleepMethod)
    assert.Equal(t, "PUT", mc.WakeMethod)
}
```

**File: `proxy/process_test.go`**

```go
func TestProcess_HTTPSleepRequest(t *testing.T) {
    // Create mock HTTP server
    sleepCalled := false
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/sleep" && r.Method == "POST" {
            // Read body
            body, _ := io.ReadAll(r.Body)
            assert.Equal(t, `{"level": "1"}`, string(body))
            assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

            sleepCalled = true
            w.WriteHeader(http.StatusOK)
            return
        }
        w.WriteHeader(http.StatusNotFound)
    }))
    defer server.Close()

    // Create process with HTTP endpoint config
    config := config.ModelConfig{
        Proxy:         server.URL,
        SleepEndpoint: "/sleep",
        SleepMethod:   "POST",
        SleepBody:     `{"level": "1"}`,
    }

    process := &Process{
        ID:                "test",
        config:            config,
        proxyLogger:       testLogger,
        globalSleepTimeout: 60,
    }

    // Execute sleep request
    err := process.sendSleepRequest()
    assert.NoError(t, err)
    assert.True(t, sleepCalled)
}

func TestProcess_HTTPWakeRequest(t *testing.T) {
    wakeCalled := false
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/wake_up" && r.Method == "POST" {
            wakeCalled = true
            w.WriteHeader(http.StatusOK)
            return
        }
        w.WriteHeader(http.StatusNotFound)
    }))
    defer server.Close()

    config := config.ModelConfig{
        Proxy:        server.URL,
        WakeEndpoint: "/wake_up",
        WakeMethod:   "POST",
    }

    process := &Process{
        ID:               "test",
        config:           config,
        proxyLogger:      testLogger,
        globalWakeTimeout: 60,
    }

    err := process.sendWakeRequest()
    assert.NoError(t, err)
    assert.True(t, wakeCalled)
}

func TestProcess_HTTPSleepErrorHandling(t *testing.T) {
    tests := []struct {
        name           string
        responseStatus int
        responseBody   string
        expectError    bool
    }{
        {"success 200", http.StatusOK, "", false},
        {"success 204", http.StatusNoContent, "", false},
        {"client error 400", http.StatusBadRequest, "invalid request", true},
        {"server error 500", http.StatusInternalServerError, "server error", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(tt.responseStatus)
                w.Write([]byte(tt.responseBody))
            }))
            defer server.Close()

            config := config.ModelConfig{
                Proxy:         server.URL,
                SleepEndpoint: "/sleep",
            }

            process := &Process{
                ID:                "test",
                config:            config,
                proxyLogger:       testLogger,
                globalSleepTimeout: 60,
            }

            err := process.sendSleepRequest()
            if tt.expectError {
                assert.Error(t, err)
                if tt.responseBody != "" {
                    assert.Contains(t, err.Error(), tt.responseBody)
                }
            } else {
                assert.NoError(t, err)
            }
        })
    }
}

func TestProcess_SleepWithoutEndpointConfigured(t *testing.T) {
    // Process without sleep endpoint should return error
    config := config.ModelConfig{
        Proxy: "http://localhost:8000",
        // No sleep/wake endpoints configured
    }

    process := &Process{
        ID:          "test",
        config:      config,
        proxyLogger: testLogger,
        cmd:         &exec.Cmd{Process: &os.Process{Pid: 1234}},
    }

    // Should return false - sleep not enabled
    assert.False(t, process.isSleepEnabled())

    // Attempting to execute sleep should error
    err := process.executeSleepCommand()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "sleepEndpoint not configured")
}
```

### Integration Tests

**File: `proxy/process_integration_test.go`**

```go
func TestIntegration_HTTPSleepWakeCycle(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    // Setup mock vLLM-like server
    sleepCount := 0
    wakeCount := 0
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            w.WriteHeader(http.StatusOK)
        case "/sleep":
            sleepCount++
            w.WriteHeader(http.StatusOK)
        case "/wake_up":
            wakeCount++
            w.WriteHeader(http.StatusOK)
        case "/v1/models":
            w.WriteHeader(http.StatusOK)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "data": []map[string]string{},
            })
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer server.Close()

    // Create process with HTTP sleep/wake config
    config := config.ModelConfig{
        Cmd:           "sleep 1000",  // Long-running dummy process
        Proxy:         server.URL,
        CheckEndpoint: "/health",
        SleepEndpoint: "/sleep",
        SleepMethod:   "POST",
        SleepBody:     `{"level": "1"}`,
        WakeEndpoint:  "/wake_up",
        WakeMethod:    "POST",
    }

    logger := NewLogMonitor("test", 1000)
    process := NewProcess("test-model", 30, 60, 60, config, logger, logger)

    // Start process
    err := process.start()
    require.NoError(t, err)
    assert.Equal(t, StateReady, process.CurrentState())

    // Sleep process
    process.Sleep()
    assert.Equal(t, StateAsleep, process.CurrentState())
    assert.Equal(t, 1, sleepCount)

    // Wake process
    err = process.wake()
    require.NoError(t, err)
    assert.Equal(t, StateReady, process.CurrentState())
    assert.Equal(t, 1, wakeCount)

    // Cleanup
    process.StopImmediately()
}

func TestIntegration_HTTPSleepTimeout(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    // Server that delays response longer than timeout
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/sleep" {
            time.Sleep(5 * time.Second)  // Delay longer than test timeout
            w.WriteHeader(http.StatusOK)
            return
        }
        w.WriteHeader(http.StatusOK)
    }))
    defer server.Close()

    config := config.ModelConfig{
        Cmd:           "sleep 1000",
        Proxy:         server.URL,
        CheckEndpoint: "none",
        SleepEndpoint: "/sleep",
        SleepTimeout:  1,  // 1 second timeout
    }

    logger := NewLogMonitor("test", 1000)
    process := NewProcess("test-model", 30, 1, 60, config, logger, logger)

    err := process.start()
    require.NoError(t, err)

    // Sleep should timeout and fall back to Stop
    process.Sleep()

    // Should have attempted sleep, timed out, and stopped
    time.Sleep(2 * time.Second)
    assert.Equal(t, StateStopped, process.CurrentState())
}
```

### Manual Testing

1. **vLLM with HTTP endpoints**:
   ```bash
   # Start vLLM with dev mode
   export VLLM_SERVER_DEV_MODE=1
   vllm serve model-name --port 8000

   # Configure llama-swap with HTTP endpoints
   # config.yaml:
   # models:
   #   "test-model":
   #     sleepEndpoint: /sleep
   #     sleepMethod: POST
   #     sleepBody: '{"level": "1"}'
   #     wakeEndpoint: /wake_up
   #     wakeMethod: POST

   # Start llama-swap and test sleep/wake cycle
   # Measure latencies
   ```

2. **Different HTTP methods**:
   ```bash
   # Test with PUT method
   # Test with PATCH method
   # Test GET method (if server supports)
   # Verify all work correctly
   ```

3. **Error scenarios**:
   - Invalid endpoint URLs (verify error messages)
   - Server returns 4xx/5xx errors (verify proper handling)
   - Timeout scenarios (verify fallback to Stop)
   - Network issues (server unreachable)

## Implementation Checklist

### Phase 1: Configuration Changes
- [ ] Add new fields to `ModelConfig` struct:
  - [ ] `SleepEndpoint string`
  - [ ] `SleepMethod string`
  - [ ] `SleepBody string`
  - [ ] `SleepTimeout int`
  - [ ] `WakeEndpoint string`
  - [ ] `WakeMethod string`
  - [ ] `WakeBody string`
  - [ ] `WakeTimeout int`
- [ ] Update `UnmarshalYAML` with validation logic
- [ ] Add default values (POST for methods)
- [ ] Add method normalization (lowercase → uppercase)
- [ ] Add configuration validation tests
- [ ] Delete old fields from struct:
  - [ ] Remove `CmdSleep`
  - [ ] Remove `CmdWake`
  - [ ] Remove `CmdSleepTimeout`
  - [ ] Remove `CmdWakeTimeout`

### Phase 2: HTTP Request Implementation
- [ ] Implement `sendSleepRequest()` method
  - [ ] URL construction from proxy + endpoint
  - [ ] HTTP client with configurable timeout
  - [ ] Request body handling (optional JSON)
  - [ ] Method support (GET, POST, PUT, PATCH)
  - [ ] Error handling with status codes
  - [ ] Response body logging on errors
- [ ] Implement `sendWakeRequest()` method (similar to sleep)
- [ ] Add unit tests for both methods
- [ ] Test error handling (4xx, 5xx, timeouts, network errors)

### Phase 3: Integration with Existing Code
- [ ] Replace `executeSleepCommand()`:
  - [ ] Remove all command execution logic
  - [ ] Only use HTTP endpoint
  - [ ] Remove PID substitution logic
- [ ] Replace `cmdWakeUpstreamProcess()`:
  - [ ] Remove all command execution logic
  - [ ] Only use HTTP endpoint
  - [ ] Remove PID substitution logic
- [ ] Simplify `getSleepTimeout()` to only check new fields
- [ ] Simplify `getWakeTimeout()` to only check new fields
- [ ] Simplify `isSleepEnabled()` to only check endpoints
- [ ] Remove any command sanitization imports if no longer needed

### Phase 4: Testing
- [ ] Unit tests for configuration validation
- [ ] Unit tests for HTTP request methods
- [ ] Unit tests for error handling (4xx, 5xx, timeouts)
- [ ] Integration test: full sleep/wake cycle with HTTP
- [ ] Integration test: timeout handling
- [ ] Integration test: different HTTP methods (PUT, PATCH)
- [ ] Run `make test-dev` and fix issues
- [ ] Run `make test-all` for concurrency tests

### Phase 5: Documentation
- [ ] Update configuration documentation with HTTP endpoints
- [ ] Add examples with vLLM HTTP endpoints
- [ ] Update all example config files (remove old cmdSleep/cmdWake)
- [ ] Update README with new syntax
- [ ] Add troubleshooting section
- [ ] Update CHANGELOG.md with breaking change notice

### Phase 6: Validation & Cleanup
- [ ] Manual testing with real vLLM instance
- [ ] Performance testing (measure HTTP request latency)
- [ ] Code review for security (input validation, HTTP injection)
- [ ] Verify all config files updated (no old syntax remaining)
- [ ] Update any example YAML files in tests
- [ ] Search codebase for any remaining references to cmdSleep/cmdWake

## Migration Timeline

### Current Release (Breaking Change)
- Remove cmdSleep/cmdWake fields entirely
- Only support HTTP endpoint configuration
- Update all documentation and examples
- Add CHANGELOG entry explaining breaking change

### User Migration Required
Users with existing cmdSleep/cmdWake configurations must update their configs:

**Old:**
```yaml
cmdSleep: curl -X POST http://localhost:${PORT}/sleep -d '{"level": "1"}'
cmdWake: curl -X POST http://localhost:${PORT}/wake_up
```

**New:**
```yaml
sleepEndpoint: /sleep
sleepMethod: POST
sleepBody: '{"level": "1"}'
wakeEndpoint: /wake_up
wakeMethod: POST
```

## Benefits Summary

### Security
- ✅ Eliminates shell command execution
- ✅ No command injection vulnerabilities
- ✅ Input validation on HTTP method/endpoint

### Performance
- ✅ Faster execution (no shell spawning)
- ✅ Reduced latency for sleep/wake operations
- ✅ More efficient resource usage

### Usability
- ✅ Simpler configuration (no curl syntax)
- ✅ Clearer error messages (HTTP status codes)
- ✅ Consistent with health check pattern
- ✅ Better IDE support (structured YAML)

### Maintainability
- ✅ Less code complexity
- ✅ Easier to test (HTTP mocking)
- ✅ Better error handling
- ✅ More predictable behavior

## References

- Current implementation: `proxy/process.go` lines 560-703
- Health check pattern: `proxy/process.go` lines 727-759
- Configuration structure: `proxy/config/model_config.go`
- vLLM sleep API: https://docs.vllm.ai/en/stable/features/sleep_mode.html
