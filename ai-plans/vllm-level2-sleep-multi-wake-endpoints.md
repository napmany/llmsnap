# vLLM Level 2 Sleep with Multi-Step Wake Support

## Title
Add support for multiple sequential wake endpoints to enable vLLM level 2 sleep mode

## Overview

Currently, llama-swap supports vLLM level 1 sleep mode, which requires only a single wake endpoint (`/wake_up`). However, vLLM level 2 sleep mode requires **three sequential HTTP requests** to fully wake a model:

1. `POST /wake_up` - Wake the model from sleep
2. `POST /collective_rpc` with body `{"method": "reload_weights"}` - Reload model weights from disk
3. `POST /reset_prefix_cache` - Reset the prefix cache

According to the vLLM blog post (https://blog.vllm.ai/2025/10/26/sleep-mode.html):
- **Level 1** offloads weights to CPU RAM (faster wake: 0.1-6s, higher RAM usage)
- **Level 2** discards weights entirely (slower wake: 0.8-2.6s, minimal RAM usage)

Level 2 achieves **65% faster switching** compared to no sleep mode while using minimal CPU RAM, making it ideal for memory-constrained environments.

### Current Limitation

The existing HTTP endpoint configuration only supports a single wake endpoint:

```yaml
wakeEndpoint: /wake_up
wakeMethod: POST
wakeBody: '{}'
```

This works for level 1 but cannot handle level 2's multi-step wake process.

### Proposed Solution

Migrate from single wake endpoint configuration to **array-based wake configuration** that supports multiple sequential requests:

```yaml
# Level 1 sleep (single endpoint)
wakeEndpoints:
  - endpoint: /wake_up
    method: POST

# Level 2 sleep (multiple endpoints)
wakeEndpoints:
  - endpoint: /wake_up
    method: POST
  - endpoint: /collective_rpc
    method: POST
    body: '{"method": "reload_weights"}'
  - endpoint: /reset_prefix_cache
    method: POST
```

This approach:
- **Breaking change**: Old single-endpoint configs must be migrated
- **Sequential execution**: Endpoints called in order, each must succeed
- **Flexible**: Works for any multi-step wake process
- **Clear semantics**: Array order matches execution order
- **Same pattern for sleep**: Apply same approach to `sleepEndpoints` for consistency

## Design Requirements

### 1. Configuration Schema Changes

**File: `proxy/config/model_config.go`**

Add new array-based endpoint configuration structures:

```go
// HTTPEndpoint represents a single HTTP endpoint configuration
type HTTPEndpoint struct {
    Endpoint string `yaml:"endpoint"` // URL path (e.g., "/wake_up")
    Method   string `yaml:"method"`   // HTTP method (GET, POST, PUT, PATCH)
    Body     string `yaml:"body"`     // Optional request body (JSON string)
    Timeout  int    `yaml:"timeout"`  // Optional per-endpoint timeout (seconds)
}

type ModelConfig struct {
    // ... existing fields ...

    // REMOVED (breaking change):
    // - SleepEndpoint, SleepMethod, SleepBody, SleepTimeout
    // - WakeEndpoint, WakeMethod, WakeBody, WakeTimeout

    // Array-based configuration (REQUIRED for sleep/wake)
    SleepEndpoints []HTTPEndpoint `yaml:"sleepEndpoints"`
    WakeEndpoints  []HTTPEndpoint `yaml:"wakeEndpoints"`
}
```

### 2. Configuration Parsing and Validation

**File: `proxy/config/model_config.go`**

Update `UnmarshalYAML` to:

1. **Parse array-based configuration**
2. **Validate array configurations**
3. **Set defaults for each endpoint**

```go
func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
    type rawModelConfig ModelConfig
    defaults := rawModelConfig{
        // ... existing defaults ...
    }

    if err := unmarshal(&defaults); err != nil {
        return err
    }

    *m = ModelConfig(defaults)

    // Validation: if one is set, both must be set
    hasSleep := len(m.SleepEndpoints) > 0
    hasWake := len(m.WakeEndpoints) > 0

    if hasSleep && !hasWake {
        return errors.New("wakeEndpoints required when sleepEndpoints is configured")
    }
    if hasWake && !hasSleep {
        return errors.New("sleepEndpoints required when wakeEndpoints is configured")
    }

    // Validate and normalize each endpoint
    for i := range m.SleepEndpoints {
        if err := m.validateEndpoint(&m.SleepEndpoints[i], "sleep"); err != nil {
            return fmt.Errorf("sleepEndpoints[%d]: %v", i, err)
        }
    }

    for i := range m.WakeEndpoints {
        if err := m.validateEndpoint(&m.WakeEndpoints[i], "wake"); err != nil {
            return fmt.Errorf("wakeEndpoints[%d]: %v", i, err)
        }
    }

    return nil
}

func (m *ModelConfig) validateEndpoint(ep *HTTPEndpoint, context string) error {
    // Endpoint path is required
    if ep.Endpoint == "" {
        return errors.New("endpoint path is required")
    }

    // Default method to POST if not specified
    if ep.Method == "" {
        ep.Method = "POST"
    }

    // Validate HTTP method
    validMethods := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true}
    upperMethod := strings.ToUpper(ep.Method)
    if !validMethods[upperMethod] {
        return fmt.Errorf("invalid method %q (must be GET, POST, PUT, or PATCH)", ep.Method)
    }
    ep.Method = upperMethod

    // Timeout validation (must be non-negative)
    if ep.Timeout < 0 {
        return fmt.Errorf("timeout must be non-negative, got %d", ep.Timeout)
    }

    return nil
}
```

### 3. Process Implementation Changes

**File: `proxy/process.go`**

#### 3.1 Update Helper Methods

Replace single-endpoint HTTP methods with array-based versions:

```go
// sendHTTPRequest sends a single HTTP request based on endpoint config
func (p *Process) sendHTTPRequest(endpoint HTTPEndpoint, operationName string, defaultTimeout time.Duration) error {
    if endpoint.Endpoint == "" {
        return fmt.Errorf("%s endpoint not configured", operationName)
    }

    // Build full URL from proxy base + endpoint
    baseURL, err := url.Parse(p.config.Proxy)
    if err != nil {
        return fmt.Errorf("failed to parse proxy URL: %v", err)
    }

    endpointURL, err := url.Parse(endpoint.Endpoint)
    if err != nil {
        return fmt.Errorf("failed to parse endpoint: %v", err)
    }

    fullURL := baseURL.ResolveReference(endpointURL).String()

    // Determine timeout (endpoint-specific or default)
    timeout := defaultTimeout
    if endpoint.Timeout > 0 {
        timeout = time.Duration(endpoint.Timeout) * time.Second
    }

    // Create HTTP client with timeout
    client := &http.Client{
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout: 2 * time.Second, // Connection timeout
            }).DialContext,
        },
        Timeout: timeout,
    }

    // Prepare request body if configured
    var bodyReader io.Reader
    if endpoint.Body != "" {
        bodyReader = strings.NewReader(endpoint.Body)
    }

    // Create and execute request
    req, err := http.NewRequest(endpoint.Method, fullURL, bodyReader)
    if err != nil {
        return fmt.Errorf("failed to create %s request: %v", operationName, err)
    }

    if endpoint.Body != "" {
        req.Header.Set("Content-Type", "application/json")
    }

    p.proxyLogger.Debugf("<%s> Sending %s request: %s %s", p.ID, operationName, endpoint.Method, fullURL)
    if endpoint.Body != "" {
        p.proxyLogger.Debugf("<%s> Request body: %s", p.ID, endpoint.Body)
    }

    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("%s request failed: %v", operationName, err)
    }
    defer resp.Body.Close()

    // Check response status
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("%s request returned status %d: %s", operationName, resp.StatusCode, string(body))
    }

    p.proxyLogger.Debugf("<%s> %s request successful (status %d)", p.ID, operationName, resp.StatusCode)
    return nil
}

// sendSleepRequests sends all sleep requests in sequence
func (p *Process) sendSleepRequests() error {
    if len(p.config.SleepEndpoints) == 0 {
        return fmt.Errorf("no sleep endpoints configured")
    }

    defaultTimeout := p.getSleepTimeout()
    p.proxyLogger.Infof("<%s> Executing %d sleep request(s) (default timeout: %v)",
        p.ID, len(p.config.SleepEndpoints), defaultTimeout)

    for i, endpoint := range p.config.SleepEndpoints {
        p.proxyLogger.Debugf("<%s> Sleep step %d/%d: %s %s",
            p.ID, i+1, len(p.config.SleepEndpoints), endpoint.Method, endpoint.Endpoint)

        if err := p.sendHTTPRequest(endpoint, "sleep", defaultTimeout); err != nil {
            return fmt.Errorf("sleep step %d/%d failed: %v", i+1, len(p.config.SleepEndpoints), err)
        }
    }

    p.proxyLogger.Infof("<%s> All %d sleep request(s) completed successfully",
        p.ID, len(p.config.SleepEndpoints))
    return nil
}

// sendWakeRequests sends all wake requests in sequence
func (p *Process) sendWakeRequests() error {
    if len(p.config.WakeEndpoints) == 0 {
        return fmt.Errorf("no wake endpoints configured")
    }

    defaultTimeout := p.getWakeTimeout()
    p.proxyLogger.Infof("<%s> Executing %d wake request(s) (default timeout: %v)",
        p.ID, len(p.config.WakeEndpoints), defaultTimeout)

    for i, endpoint := range p.config.WakeEndpoints {
        p.proxyLogger.Debugf("<%s> Wake step %d/%d: %s %s",
            p.ID, i+1, len(p.config.WakeEndpoints), endpoint.Method, endpoint.Endpoint)

        if err := p.sendHTTPRequest(endpoint, "wake", defaultTimeout); err != nil {
            return fmt.Errorf("wake step %d/%d failed: %v", i+1, len(p.config.WakeEndpoints), err)
        }
    }

    p.proxyLogger.Infof("<%s> All %d wake request(s) completed successfully",
        p.ID, len(p.config.WakeEndpoints))
    return nil
}
```

#### 3.2 Update executeSleepCommand and cmdWakeUpstreamProcess

```go
// executeSleepCommand executes sleep operation via HTTP endpoints
func (p *Process) executeSleepCommand() error {
    if p.cmd == nil || p.cmd.Process == nil {
        return fmt.Errorf("process is nil, cannot execute sleep")
    }

    if len(p.config.SleepEndpoints) == 0 {
        return fmt.Errorf("no sleep endpoints configured")
    }

    if err := p.sendSleepRequests(); err != nil {
        return fmt.Errorf("HTTP sleep requests failed: %v", err)
    }

    return nil
}

// cmdWakeUpstreamProcess wakes the upstream process via HTTP endpoints
func (p *Process) cmdWakeUpstreamProcess() error {
    p.processLogger.Debugf("<%s> cmdWakeUpstreamProcess() initiating wake", p.ID)

    if p.cmd == nil || p.cmd.Process == nil {
        return fmt.Errorf("process is nil, cannot execute wake")
    }

    if len(p.config.WakeEndpoints) == 0 {
        return fmt.Errorf("no wake endpoints configured")
    }

    if err := p.sendWakeRequests(); err != nil {
        return fmt.Errorf("HTTP wake requests failed: %v", err)
    }

    return nil
}
```

#### 3.3 Update isSleepEnabled

```go
// isSleepEnabled returns true if sleep/wake endpoints are configured
func (p *Process) isSleepEnabled() bool {
    return len(p.config.SleepEndpoints) > 0 && len(p.config.WakeEndpoints) > 0
}
```

### 4. Configuration Examples

**Example: vLLM Level 1 Sleep (single endpoint)**

```yaml
models:
  phi-4-quantized.w4a16:
    cmd: "vllm serve ./phi-4-quantized.w4a16/ --port ${PORT} --enable-sleep-mode"
    env:
      - VLLM_SERVER_DEV_MODE=1

    sleepEndpoints:
      - endpoint: /sleep
        method: POST
        body: '{"level": 1}'

    wakeEndpoints:
      - endpoint: /wake_up
        method: POST
```

**Example: vLLM Level 2 Sleep (multiple endpoints)**

```yaml
models:
  phi-4-quantized.w4a16:
    cmd: "vllm serve ./phi-4-quantized.w4a16/ --port ${PORT} --enable-sleep-mode"
    env:
      - VLLM_SERVER_DEV_MODE=1

    sleepEndpoints:
      - endpoint: /sleep
        method: POST
        body: '{"level": 2}'

    wakeEndpoints:
      # Step 1: Wake the model
      - endpoint: /wake_up
        method: POST

      # Step 2: Reload weights from disk
      - endpoint: /collective_rpc
        method: POST
        body: '{"method": "reload_weights"}'

      # Step 3: Reset the prefix cache
      - endpoint: /reset_prefix_cache
        method: POST
```

**Example: Custom timeouts per endpoint**

```yaml
models:
  large-model:
    sleepEndpoints:
      - endpoint: /sleep
        method: POST
        timeout: 120  # 2 minutes for large model

    wakeEndpoints:
      - endpoint: /wake_up
        method: POST
        timeout: 30  # Quick wake

      - endpoint: /collective_rpc
        method: POST
        body: '{"method": "reload_weights"}'
        timeout: 180  # 3 minutes to reload large weights

      - endpoint: /reset_prefix_cache
        method: POST
        timeout: 10  # Fast cache reset
```

### 5. Error Handling

**Sequential execution with fail-fast behavior:**

1. **Sleep requests**: Execute in array order, stop on first failure
   - If any step fails → fall back to `Stop()` and log error
   - All steps must succeed for sleep to be considered successful

2. **Wake requests**: Execute in array order, stop on first failure
   - If any step fails → transition to `StateStopping`, then restart with `start()`
   - All steps must succeed for wake to be considered successful
   - Track which step failed for better error messages

3. **Timeout handling**: Each endpoint can have its own timeout
   - Per-endpoint timeout takes precedence
   - Falls back to global sleep/wake timeout
   - Falls back to 60-second default if neither configured

**Logging for multi-step operations:**

```go
// Example log output for level 2 wake:
// <%s> Executing 3 wake request(s) (default timeout: 60s)
// <%s> Wake step 1/3: POST /wake_up
// <%s> Wake request successful (status 200)
// <%s> Wake step 2/3: POST /collective_rpc
// <%s> Request body: {"method": "reload_weights"}
// <%s> Wake request successful (status 200)
// <%s> Wake step 3/3: POST /reset_prefix_cache
// <%s> Wake request successful (status 200)
// <%s> All 3 wake request(s) completed successfully
// <%s> Model wake completed in 1.234s
```

If a step fails:
```go
// <%s> Wake step 2/3: POST /collective_rpc
// <%s> Wake request failed: wake request returned status 500: Internal Server Error
// <%s> cmdWake failed: wake step 2/3 failed: wake request returned status 500: Internal Server Error
// <%s> Falling back to full process restart
```

### 6. Breaking Change and Migration

**BREAKING CHANGE**: Old configuration format is **completely removed**.

**Old format (NO LONGER SUPPORTED)**:
```yaml
sleepEndpoint: /sleep
sleepMethod: POST
sleepBody: '{"level": 1}'
wakeEndpoint: /wake_up
wakeMethod: POST
```

**New format (REQUIRED)**:
```yaml
sleepEndpoints:
  - endpoint: /sleep
    method: POST
    body: '{"level": 1}'
wakeEndpoints:
  - endpoint: /wake_up
    method: POST
```

**Migration required for all users** with sleep/wake configured:
1. Replace `sleepEndpoint` → `sleepEndpoints` (array)
2. Replace `wakeEndpoint` → `wakeEndpoints` (array)
3. Move `sleepMethod` → `method` (inside endpoint object)
4. Move `sleepBody` → `body` (inside endpoint object)
5. Move `sleepTimeout` → `timeout` (inside endpoint object, optional)
6. Same pattern for wake endpoints

**Error if old format detected**:
- Config will fail to load with clear error message
- Error message should point to migration documentation

## Testing Plan

### Unit Tests

**File: `proxy/config/model_config_test.go`**

1. **Configuration Parsing Tests**:
   ```go
   func TestModelConfig_MultipleWakeEndpoints(t *testing.T)
   func TestModelConfig_EndpointValidation(t *testing.T)
   func TestModelConfig_DefaultMethodAndTimeout(t *testing.T)
   func TestModelConfig_InvalidMethod(t *testing.T)
   func TestModelConfig_RequireBothSleepAndWake(t *testing.T)
   ```

2. **Test cases**:
   - Parse array with 1 endpoint (level 1)
   - Parse array with 3 endpoints (level 2)
   - Reject invalid HTTP methods
   - Apply default method (POST) when not specified
   - Validate timeout values (non-negative)
   - Require both sleep and wake if one is configured
   - Empty endpoint path should error
   - Negative timeout should error

**File: `proxy/process_test.go`**

1. **HTTP Request Tests**:
   ```go
   func TestProcess_SendHTTPRequest(t *testing.T)
   func TestProcess_SendMultipleWakeRequests(t *testing.T)
   func TestProcess_WakeRequestSequentialFailure(t *testing.T)
   func TestProcess_CustomTimeoutPerEndpoint(t *testing.T)
   func TestProcess_RequestBodyHandling(t *testing.T)
   ```

2. **Test cases**:
   - Send single HTTP request successfully
   - Send 3 sequential wake requests (level 2)
   - Verify requests sent in correct order
   - Fail on second request, verify error includes step number
   - Verify custom timeout applied per endpoint
   - Verify JSON body sent with correct Content-Type

### Integration Tests

**File: `proxy/process_integration_test.go`**

1. **Full Level 2 Sleep/Wake Cycle**:
   ```go
   func TestIntegration_VLLMLevel2SleepWake(t *testing.T) {
       // Mock server that tracks all 3 wake requests
       // Verify all 3 endpoints called in sequence
       // Verify process state transitions correctly
       // Verify PID remains same throughout
   }
   ```

2. **Partial Failure Handling**:
   ```go
   func TestIntegration_WakePartialFailure(t *testing.T) {
       // Mock server: first request succeeds, second fails
       // Verify wake aborts after failure
       // Verify process restarts (not stuck in broken state)
   }
   ```

3. **Mixed Configuration Test**:
   ```go
   func TestIntegration_MixedLevel1AndLevel2Models(t *testing.T) {
       // Model A: level 1 (single wake endpoint in array)
       // Model B: level 2 (three wake endpoints in array)
       // Swap between them, verify both work correctly
   }
   ```

### Manual Testing with Real vLLM

1. **Setup vLLM with level 2 sleep**:
   ```bash
   export VLLM_SERVER_DEV_MODE=1
   vllm serve model-name --port 8000 --enable-sleep-mode
   ```

2. **Test level 2 sleep/wake cycle**:
   - Configure llama-swap with 3 wake endpoints
   - Trigger sleep via TTL expiration
   - Make new request to trigger wake
   - Verify all 3 wake endpoints called
   - Measure wake latency (should be 0.8-2.6s per blog post)
   - Verify model responds correctly after wake

3. **Test error scenarios**:
   - Kill vLLM process while asleep (wake should fail and restart)
   - Invalid `collective_rpc` body (wake should fail at step 2)
   - Network timeout on step 3 (verify proper timeout handling)

4. **Performance testing**:
   - Compare level 1 vs level 2 wake times
   - Measure memory usage difference
   - Test rapid swap between models (verify no degradation)

### Regression Testing

1. **Verify level 1 configs work with new format**:
   - Array format with 1 endpoint
   - Ensure no regressions from previous behavior

2. **Run full test suite**:
   ```bash
   make test-dev   # Quick tests
   make test-all   # Full concurrency tests
   ```

## Implementation Checklist

### Phase 1: Core Data Structures ✅
- [x] Add `HTTPEndpoint` struct to `model_config.go`
  - [x] `Endpoint string`
  - [x] `Method string`
  - [x] `Body string`
  - [x] `Timeout int`
- [x] Add `SleepEndpoints []HTTPEndpoint` to `ModelConfig`
- [x] Add `WakeEndpoints []HTTPEndpoint` to `ModelConfig`
- [x] Remove old fields completely (breaking change):
  - [x] Remove `SleepEndpoint`, `SleepMethod`, `SleepBody`, `SleepTimeout`
  - [x] Remove `WakeEndpoint`, `WakeMethod`, `WakeBody`, `WakeTimeout`

### Phase 2: Configuration Parsing ✅
- [x] Update `UnmarshalYAML` in `model_config.go`
  - [x] Parse array-based configuration only
  - [x] Validate endpoint arrays
  - [x] No backward compatibility code
- [x] Implement `validateEndpoint()` method
  - [x] Validate endpoint path is not empty
  - [x] Default method to POST
  - [x] Validate HTTP method (GET, POST, PUT, PATCH)
  - [x] Normalize method to uppercase
  - [x] Validate timeout is non-negative
- [x] Add validation: both sleep and wake required if one is configured
- [x] Update macro substitution in `config.go` to support array-based endpoints
- [ ] Add unit tests for configuration parsing

### Phase 3: HTTP Request Implementation ✅
- [x] Implement `sendHTTPRequest()` in `process.go`
  - [x] Build full URL from proxy + endpoint
  - [x] Create HTTP client with configurable timeout
  - [x] Handle request body (optional JSON)
  - [x] Send request with proper method
  - [x] Check response status codes
  - [x] Log request details (debug level)
  - [x] Return detailed error messages
- [x] Implement `sendSleepRequests()` in `process.go`
  - [x] Loop through sleep endpoints array
  - [x] Call each endpoint sequentially
  - [x] Stop on first failure (fail-fast)
  - [x] Log progress for each step
- [x] Implement `sendWakeRequests()` in `process.go`
  - [x] Loop through wake endpoints array
  - [x] Call each endpoint sequentially
  - [x] Stop on first failure (fail-fast)
  - [x] Log progress for each step
  - [x] Include step number in error messages

### Phase 4: Integration with Existing Code ✅
- [x] Update `executeSleepCommand()` to use `sendSleepRequests()`
- [x] Update `cmdWakeUpstreamProcess()` to use `sendWakeRequests()`
- [x] Update `isSleepEnabled()` to check array length
- [x] Ensure error messages include step context
- [x] Update `getSleepTimeout()` and `getWakeTimeout()` to work with new format

### Phase 5: Configuration Files ✅
- [x] Update `config-lvl2.yaml` with proper 3-endpoint wake config
  - [x] Remove warning comment about unsupported feature
  - [x] Add all 3 wake endpoints
  - [x] Add helpful comments explaining each step
- [x] Update `config-lvl1.yaml` to use array-based format
- [ ] Update `config.example.yaml` with both level 1 and level 2 examples
- [ ] Create new example: `config-sleep-examples.yaml` showing:
  - [ ] Level 1 (single endpoint)
  - [ ] Level 2 (three endpoints)
  - [ ] Custom timeouts per endpoint

### Phase 6: Testing ✅ (Core tests pass)
- [ ] Unit tests for `HTTPEndpoint` validation
- [ ] Unit tests for array-based config parsing
- [ ] Unit tests for `sendHTTPRequest()`
- [ ] Unit tests for `sendSleepRequests()` and `sendWakeRequests()`
- [ ] Integration test: mock server with 3 wake endpoints
- [ ] Integration test: verify sequential execution and failure handling
- [ ] Integration test: level 1 and level 2 models swapping
- [x] Run `make test-dev` and fix any issues
- [ ] Run `make test-all` for concurrency tests

### Phase 7: Documentation
- [ ] Update README.md with level 2 sleep support
- [ ] Add configuration documentation:
  - [ ] Explain `sleepEndpoints` array format
  - [ ] Explain `wakeEndpoints` array format
  - [ ] Document `HTTPEndpoint` struct fields
  - [ ] Show level 1 vs level 2 examples
  - [ ] Explain sequential execution behavior
  - [ ] Document error handling (fail-fast)
  - [ ] Show custom timeout examples
- [ ] Update CHANGELOG.md with **BREAKING CHANGE** notice
- [ ] Add migration guide showing exact transformation from old → new format
- [ ] Document vLLM level 2 sleep requirements
- [ ] Add troubleshooting section for multi-step wake failures
- [ ] Clearly document that old format is no longer supported

### Phase 8: Manual Validation
- [ ] Test with real vLLM instance (level 1)
- [ ] Test with real vLLM instance (level 2)
- [ ] Measure actual wake times (verify blog post claims)
- [ ] Test error scenarios (failed step 2, timeout on step 3, etc.)
- [ ] Verify old config format produces clear error message
- [ ] Performance testing: rapid model swaps
- [ ] Memory usage monitoring (level 1 vs level 2)

## Expected Outcomes

### Performance Improvements
- **Level 2 wake time**: 0.8-2.6 seconds (per vLLM blog)
- **Level 2 vs no sleep**: 65% faster model switching
- **Level 2 vs level 1**: Slightly slower wake but minimal RAM usage
- **Sequential requests overhead**: ~10-50ms per additional endpoint

### Memory Savings
- **Level 1**: GPU offloaded, weights in CPU RAM (high RAM usage)
- **Level 2**: GPU offloaded, weights discarded (minimal RAM usage)
- **Example**: 70B model level 2 uses ~95% less RAM than level 1

### User Benefits
- **Memory-constrained systems**: Can run larger models with level 2
- **Multi-model setups**: More models can be kept "warm" in sleep state
- **Faster iteration**: Developers can swap between models quickly
- **Cost savings**: Less memory → smaller instances → lower costs

## Known Limitations

1. **Sequential execution only**: Endpoints executed one at a time (not parallel)
   - This is intentional: vLLM level 2 requires sequential order
   - Future enhancement: support parallel requests if needed

2. **No retry logic**: Failed step immediately aborts the operation
   - Falls back to full restart on wake failure
   - Future enhancement: configurable retry with exponential backoff

3. **All-or-nothing**: All endpoints must succeed
   - No partial wake state
   - Future enhancement: configurable partial success handling

4. **Fixed error handling**: Failed sleep → Stop(), failed wake → restart
   - No customization of fallback behavior
   - Future enhancement: configurable error handlers

## Future Enhancements

1. **Parallel request support**: Execute multiple endpoints concurrently
   - Use case: Independent initialization steps
   - Config: `parallel: true` flag on endpoint

2. **Retry logic**: Automatic retry with exponential backoff
   - Config: `retries: 3` and `retryDelay: 1s`
   - Per-endpoint or global configuration

3. **Conditional execution**: Skip endpoints based on response
   - Use case: "Only call step 2 if step 1 returned specific status"
   - Config: `if: "prev.status == 200"`

4. **Response validation**: Check response body, not just status code
   - Use case: Verify wake actually succeeded
   - Config: `expectedBody: '{"status": "ready"}'`

5. **Metrics tracking**: Record per-step latencies
   - Emit events for each endpoint call
   - Dashboard showing which step is slowest

6. **Health checks between steps**: Verify model health after critical steps
   - Use case: Check health after reload_weights
   - Config: `healthCheckAfter: true`

## References

- vLLM Sleep Mode Blog: https://blog.vllm.ai/2025/10/26/sleep-mode.html
- vLLM Sleep Mode Docs: https://docs.vllm.ai/en/stable/features/sleep_mode.html
- Current sleep/wake implementation: `proxy/process.go:495-818`
- Current config structure: `proxy/config/model_config.go:10-52`
- Migration plan (command → HTTP): `ai-plans/migrate-sleep-wake-to-http-endpoints.md`
- Original sleep mode plan: `ai-plans/vllm-sleep-mode-support.md`
