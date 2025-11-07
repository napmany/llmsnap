# Sleep/Wake Timeout Configuration Fix

## Title
Make sleep/wake command timeouts configurable to prevent premature termination

## Overview

The current implementation uses hardcoded timeouts for sleep (10s) and wake (30s) commands. These timeouts are too aggressive for larger models, causing sleep operations to be interrupted mid-execution and falling back to process termination instead of sleeping.

**Observed Issue:**
When swapping from a large model (gpt-oss-20b) that uses sleep mode, the cmdSleep operation times out after 10 seconds. The vLLM server is actively performing the sleep operation (resetting prefix cache, offloading weights), but gets interrupted by the timeout. This causes:
1. The curl command to be killed after 10s
2. llama-swap to fall back to Stop() and kill the process
3. vLLM to log errors about interrupted sleep operation
4. Complete process termination instead of sleeping (defeating the purpose of sleep mode)

**Root Cause:**
- Sleep timeout hardcoded to 10s at `proxy/process.go:496`
- Wake timeout hardcoded to 30s at `proxy/process.go:600`
- Large models require more time for sleep operations (especially level 1 which offloads to CPU)

## Design Requirements

### 1. Add Configuration Fields

Add new optional timeout fields to global config and model config:

**Global Config** (`proxy/config/config.go`):
```yaml
# Global defaults for sleep/wake timeouts
cmdSleepTimeout: 60   # Default timeout for cmdSleep execution (seconds)
cmdWakeTimeout: 60    # Default timeout for cmdWake execution (seconds)
```

**Model Config** (`proxy/config/model_config.go`):
```yaml
models:
  "large-model":
    cmd: ...
    cmdSleep: curl -X POST http://localhost:${PORT}/sleep -d '{"level": "1"}'
    cmdWake: curl -X POST http://localhost:${PORT}/wake_up
    cmdSleepTimeout: 120  # Override global timeout for this specific model
    cmdWakeTimeout: 90    # Override global timeout for this specific model
```

**New Configuration Fields**:

- **`Config.CmdSleepTimeout`** (int, optional, default: 60)
  - Global default timeout for cmdSleep execution in seconds
  - Used if model-specific timeout is not defined
  - Reasonable default for most models

- **`Config.CmdWakeTimeout`** (int, optional, default: 60)
  - Global default timeout for cmdWake execution in seconds
  - Used if model-specific timeout is not defined

- **`ModelConfig.CmdSleepTimeout`** (int, optional)
  - Per-model override for sleep timeout
  - If not set, uses global `Config.CmdSleepTimeout`
  - Allows customization for larger models that need more time

- **`ModelConfig.CmdWakeTimeout`** (int, optional)
  - Per-model override for wake timeout
  - If not set, uses global `Config.CmdWakeTimeout`

### 2. Timeout Resolution Logic

When executing sleep/wake commands, resolve timeout in this order:
1. Model-specific timeout (if defined)
2. Global config timeout (if defined)
3. Hardcoded default (60s for both)

**Implementation in Process struct**:
```go
func (p *Process) getSleepTimeout() time.Duration {
    // Model-specific override
    if p.config.CmdSleepTimeout > 0 {
        return time.Duration(p.config.CmdSleepTimeout) * time.Second
    }
    // Global config (passed during NewProcess)
    if p.globalSleepTimeout > 0 {
        return time.Duration(p.globalSleepTimeout) * time.Second
    }
    // Default fallback
    return 60 * time.Second
}

func (p *Process) getWakeTimeout() time.Duration {
    // Model-specific override
    if p.config.CmdWakeTimeout > 0 {
        return time.Duration(p.config.CmdWakeTimeout) * time.Second
    }
    // Global config
    if p.globalWakeTimeout > 0 {
        return time.Duration(p.globalWakeTimeout) * time.Second
    }
    // Default fallback
    return 60 * time.Second
}
```

### 3. Update Process Struct

Add fields to `Process` struct to store global defaults:
```go
type Process struct {
    // ... existing fields ...
    globalSleepTimeout int // From config.CmdSleepTimeout
    globalWakeTimeout  int // From config.CmdWakeTimeout
}
```

Update `NewProcess()` signature to accept global timeouts:
```go
func NewProcess(
    ID string,
    healthCheckTimeout int,
    sleepTimeout int,      // NEW
    wakeTimeout int,       // NEW
    config config.ModelConfig,
    processLogger *LogMonitor,
    proxyLogger *LogMonitor,
) *Process
```

### 4. Update Sleep Command Execution

**Current code** (`proxy/process.go:495-511`):
```go
// Create command with 10s timeout (aggressive)
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
```

**New code**:
```go
// Create command with configurable timeout
sleepTimeout := p.getSleepTimeout()
ctx, cancel := context.WithTimeout(context.Background(), sleepTimeout)
defer cancel()

// ... execute command ...

if err := cmd.Run(); err != nil {
    if ctx.Err() == context.DeadlineExceeded {
        return fmt.Errorf("sleep command timed out after %v", sleepTimeout)
    }
    // ... rest of error handling ...
}
```

### 5. Update Wake Command Execution

**Current code** (`proxy/process.go:599-616`):
```go
// Create command with 30s timeout (aggressive)
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
```

**New code**:
```go
// Create command with configurable timeout
wakeTimeout := p.getWakeTimeout()
ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
defer cancel()

// ... execute command ...

if err := cmd.Run(); err != nil {
    if ctx.Err() == context.DeadlineExceeded {
        return fmt.Errorf("wake command timed out after %v", wakeTimeout)
    }
    // ... rest of error handling ...
}
```

### 6. Update ProcessGroup Creation

Update all places where `NewProcess()` is called to pass global timeout values:

**In `proxy/proxymanager.go`** (or wherever processes are created):
```go
process := NewProcess(
    modelID,
    config.HealthCheckTimeout,
    config.CmdSleepTimeout,  // NEW: pass global sleep timeout
    config.CmdWakeTimeout,   // NEW: pass global wake timeout
    modelConfig,
    processLogger,
    proxyLogger,
)
```

### 7. Logging Improvements

Update log messages to show the actual timeout used:
```go
p.proxyLogger.Infof("<%s> Executing cmdSleep (timeout: %v)", p.ID, sleepTimeout)
p.proxyLogger.Errorf("<%s> cmdSleep timed out after %v, falling back to Stop()", p.ID, sleepTimeout)

p.proxyLogger.Infof("<%s> Executing cmdWake (timeout: %v)", p.ID, wakeTimeout)
p.proxyLogger.Errorf("<%s> cmdWake timed out after %v, restarting process", p.ID, wakeTimeout)
```

### 8. Documentation Updates

Update configuration documentation to explain timeout settings:

**config.example.yaml**:
```yaml
# Global timeout settings for sleep/wake commands
# These can be overridden per-model if needed
cmdSleepTimeout: 60  # seconds, default if not specified
cmdWakeTimeout: 60   # seconds, default if not specified

models:
  "large-model-example":
    cmd: ...
    cmdSleep: curl -X POST http://localhost:${PORT}/sleep -d '{"level": "1"}'
    cmdWake: curl -X POST http://localhost:${PORT}/wake_up

    # Optional: Override timeouts for this specific model
    # Useful for very large models that need more time to sleep/wake
    cmdSleepTimeout: 120  # 2 minutes for this large model
    cmdWakeTimeout: 90    # 1.5 minutes
```

**docs/configuration.md**:
- Document new global timeout fields
- Document per-model timeout overrides
- Explain timeout resolution order
- Provide guidance on setting appropriate timeouts based on model size

## Recommended Timeout Values

Based on vLLM documentation and observed behavior:

**Small models (< 7B parameters):**
- cmdSleepTimeout: 30s
- cmdWakeTimeout: 30s

**Medium models (7B - 20B parameters):**
- cmdSleepTimeout: 60s (default)
- cmdWakeTimeout: 60s (default)

**Large models (> 20B parameters):**
- cmdSleepTimeout: 120s (2 minutes)
- cmdWakeTimeout: 90s (1.5 minutes)

**Level 1 sleep (offload to CPU):**
- May need longer sleep timeout due to memory transfer
- Faster wake times (0.1-6s)

**Level 2 sleep (discard weights):**
- Faster sleep times
- Slower wake times (0.8-2.6s, but can be longer for large models)

## Testing Plan

### Unit Tests

1. **Timeout Resolution Tests**:
   - Test default timeout values when no config provided
   - Test global timeout overrides
   - Test model-specific timeout overrides
   - Test timeout resolution order (model > global > default)

2. **Configuration Parsing Tests**:
   - Test parsing of global cmdSleepTimeout/cmdWakeTimeout
   - Test parsing of model-specific timeout overrides
   - Test validation (timeouts must be positive)
   - Test optional fields (can be omitted)

### Integration Tests

1. **Sleep Timeout Test**:
   - Create mock sleep command that takes 15 seconds
   - Set cmdSleepTimeout to 10s
   - Verify timeout occurs and fallback to Stop()
   - Set cmdSleepTimeout to 20s
   - Verify sleep completes successfully

2. **Wake Timeout Test**:
   - Create mock wake command that takes 45 seconds
   - Set cmdWakeTimeout to 30s
   - Verify timeout occurs and process restarts
   - Set cmdWakeTimeout to 60s
   - Verify wake completes successfully

3. **Timeout Override Test**:
   - Set global cmdSleepTimeout to 30s
   - Set model-specific cmdSleepTimeout to 90s
   - Verify model uses 90s timeout (not 30s)

### Manual Testing

1. **Real vLLM Testing**:
   - Test with actual large model (e.g., gpt-oss-20b)
   - Configure appropriate timeouts (120s for sleep)
   - Verify sleep completes without timeout
   - Verify wake completes successfully
   - Measure actual sleep/wake times to validate timeout settings

2. **Error Scenarios**:
   - Test sleep timeout with small timeout value
   - Verify fallback to Stop() works correctly
   - Test wake timeout with small timeout value
   - Verify process restart works correctly

## Implementation Checklist

### Phase 1: Configuration
- [x] Add `CmdSleepTimeout` field to `Config` struct with default value 60
- [x] Add `CmdWakeTimeout` field to `Config` struct with default value 60
- [x] Add `CmdSleepTimeout` field to `ModelConfig` struct (optional, no default)
- [x] Add `CmdWakeTimeout` field to `ModelConfig` struct (optional, no default)
- [x] Update config parsing tests
- [ ] Update config validation (ensure positive values if provided)

### Phase 2: Process Implementation
- [x] Add `globalSleepTimeout` field to `Process` struct
- [x] Add `globalWakeTimeout` field to `Process` struct
- [x] Update `NewProcess()` signature to accept global timeouts
- [x] Implement `getSleepTimeout()` method with resolution logic
- [x] Implement `getWakeTimeout()` method with resolution logic
- [x] Update all `NewProcess()` calls to pass global timeout values

### Phase 3: Command Execution
- [x] Replace hardcoded 10s timeout in `executeSleepCommand()` with `getSleepTimeout()`
- [x] Replace hardcoded 30s timeout in `executeWakeCommand()` with `getWakeTimeout()`
- [x] Update error messages to show actual timeout used
- [x] Update log messages to show configured timeout

### Phase 4: Testing
- [ ] Add unit tests for timeout resolution logic
- [ ] Add unit tests for configuration parsing
- [ ] Add integration tests for sleep timeout
- [ ] Add integration tests for wake timeout
- [ ] Add integration tests for timeout overrides
- [ ] Manual testing with real vLLM instance

### Phase 5: Documentation
- [x] Update `config.example.yaml` with timeout examples
- [ ] Update `docs/configuration.md` with timeout documentation
- [ ] Add recommended timeout values based on model size
- [ ] Document timeout resolution order
- [ ] Update sleep/wake documentation with timeout guidance

### Phase 6: Validation
- [x] Run `make test-dev` and fix any issues
- [ ] Run `make test-all` for full test suite
- [ ] Test with real vLLM instance and large model
- [ ] Verify no regressions in existing sleep/wake functionality
- [ ] Verify timeout fallback behavior works correctly

## Expected Behavior After Fix

**Before (current broken behavior):**
```
[INFO] <gpt-oss-20b> Executing cmdSleep
[curl starts, vLLM begins sleep operation]
[After 10s: curl killed by timeout]
[ERROR] cmdSleep failed, falling back to Stop(): sleep command timed out after 10s
[Process gets killed completely, sleep mode fails]
```

**After (fixed behavior with 120s timeout):**
```
[INFO] <gpt-oss-20b> Executing cmdSleep (timeout: 120s)
[curl starts, vLLM begins sleep operation]
[vLLM completes sleep operation after 15s]
[INFO] <gpt-oss-20b> Model sleep completed in 15.2s
[Process remains alive in sleeping state]
```

**After (with custom model timeout):**
```yaml
models:
  "gpt-oss-20b":
    cmdSleepTimeout: 180  # 3 minutes for this very large model
```

## References

- Current implementation: `proxy/process.go:495-511` (sleep), `proxy/process.go:599-616` (wake)
- vLLM sleep mode: https://docs.vllm.ai/en/stable/features/sleep_mode.html
- Original sleep/wake plan: `ai-plans/vllm-sleep-mode-support.md`
