# Generic Sleep/Wake Mode Support

## Title
Add generic sleep/wake support as an alternative to process termination

## Overview

Modern inference servers (vLLM, and potentially future versions of llama.cpp) support sleep/wake functionality that enables rapid model switching by keeping the process alive while offloading GPU resources. This plan outlines the changes needed to integrate generic sleep/wake capabilities into llama-swap's model lifecycle management.

Currently, llama-swap terminates model processes entirely when swapping. With sleep mode support, compatible servers can transition to a low-resource "sleeping" state instead, enabling near-instant wake times when the model is requested again.

### Design Philosophy

Following llama-swap's existing pattern of `cmd` and `cmdStop`, sleep/wake will be implemented using **command-based configuration** rather than hard-coded HTTP endpoints. This approach:

- **Server-agnostic**: Works with any inference server (vLLM, llama.cpp, TabbyAPI, etc.)
- **Flexible**: Users control exactly how sleep/wake is invoked (curl, scripts, custom tools)
- **Consistent**: Follows the same pattern as existing `cmd` and `cmdStop` configuration
- **Future-proof**: Adaptable to new servers and methods
- **Powerful**: Full macro support (${PORT}, ${MODEL_ID}, ${PID}, etc.)

### Example: vLLM Sleep Mode

**API Endpoints** (requires `VLLM_SERVER_DEV_MODE=1`):
- `POST /sleep` - Put model to sleep (with level parameter)
- `POST /wake_up` - Wake up sleeping model
- `GET /is_sleeping` - Check sleep status (optional)

**Sleep Levels**:
- Level 1: Offload weights to CPU RAM (0.1-6s wake time, high RAM usage)
- Level 2: Discard weights entirely (0.8-2.6s wake time, minimal RAM usage)

**Performance**: 18-200x faster model switching compared to full process restart

## Design Requirements

### 1. New Configuration Options

Add optional sleep/wake commands to `ModelConfig`, following the same pattern as `cmd` and `cmdStop`:

**Example: vLLM with Docker**
```yaml
models:
  "vllm-model":
    cmd: |
      docker run --rm --name ${MODEL_ID} --runtime=nvidia
      -p ${PORT}:8000 vllm/vllm-openai
      --model /models/my-model
    env:
      - "VLLM_SERVER_DEV_MODE=1"

    # Instead of stopping the container, put it to sleep
    cmdSleep: |
      curl -X POST http://localhost:${PORT}/sleep \
        -H "Content-Type: application/json" \
        -d '{"level": "1"}'

    # Wake the model when needed
    cmdWake: curl -X POST http://localhost:${PORT}/wake_up

    # cmdStop is still available for explicit shutdown
    cmdStop: docker stop ${MODEL_ID}
```

**Example: vLLM Direct Process**
```yaml
models:
  "vllm-direct":
    cmd: |
      vllm serve /models/my-model \
        --port ${PORT} \
        --dtype auto
    env:
      - "VLLM_SERVER_DEV_MODE=1"

    cmdSleep: curl -X POST http://localhost:${PORT}/sleep -d '{"level": "2"}'
    cmdWake: curl -X POST http://localhost:${PORT}/wake_up
```

**Example: Custom Script**
```yaml
models:
  "custom-sleep":
    cmd: /path/to/custom-server --port ${PORT}

    # Use custom scripts for complex sleep/wake logic
    cmdSleep: /path/to/scripts/sleep-model.sh ${MODEL_ID} ${PORT}
    cmdWake: /path/to/scripts/wake-model.sh ${MODEL_ID} ${PORT}
```

**New Configuration Fields**:

- **`cmdSleep`** (string, optional): Command to execute to put the model to sleep
  - If defined, used instead of `cmdStop` during model swapping
  - Supports multi-line with `|` syntax
  - Supports comments (will be stripped)
  - Full macro substitution: `${PORT}`, `${MODEL_ID}`, `${PID}`, etc.
  - Command should exit with code 0 for success
  - If command fails, fallback to `cmdStop` or default termination

- **`cmdWake`** (string, optional): Command to execute to wake the model from sleep
  - Required if `cmdSleep` is defined
  - Used when loading a sleeping model
  - Supports multi-line with `|` syntax
  - Full macro substitution
  - After wake command succeeds, health check still runs
  - If wake fails, process is restarted with `cmd`

**Behavior**:
- If `cmdSleep` is defined → sleep/wake mode enabled
- If `cmdSleep` is empty/undefined → traditional stop/start mode (current behavior)
- `cmdStop` is still used for explicit shutdown requests and fallback scenarios

### 2. Process State Extensions

Add new process states to support sleep/wake lifecycle:

**New States**:
- `StateSleeping`: Process is running but in sleep mode
- `StateWaking`: Process is transitioning from sleep to ready

**State Transitions**:
```
Current flow:
  Stopped -> Starting -> Ready -> Stopping -> Stopped

With sleep mode:
  Stopped -> Starting -> Ready -> Sleeping -> Waking -> Ready
                                   ↓
                                Stopping -> Stopped
```

**Valid Transitions**:
- `Ready` → `StateSleeping` (when swapping to another model)
- `StateSleeping` → `StateWaking` (when model is requested)
- `StateSleeping` → `StateStopping` (when explicitly stopped/shutdown)
- `StateWaking` → `StateReady` (when wake completes successfully)
- `StateWaking` → `StateStopping` (if wake fails)

### 3. Sleep/Wake Implementation

#### Sleep Process

**New Method**: `func (p *Process) Sleep() error`

1. Check if `cmdSleep` is defined in config
2. If not defined, fall back to `Stop()`
3. Wait for inflight requests to complete (like `Stop()`)
4. Validate current state is `Ready`
5. Swap state to `StateSleeping`
6. Execute `cmdSleep` using `exec.Command()`:
   - Parse and sanitize command (like `cmd` and `cmdStop`)
   - Apply macro substitution (${PORT}, ${MODEL_ID}, ${PID}, etc.)
   - Execute command with timeout (default: 30 seconds)
   - Capture stdout/stderr to processLogger
7. Check exit code:
   - Exit code 0 → success, keep process running
   - Non-zero → fall back to `Stop()` and log warning
8. Emit `ProcessSleepEvent`

**Error Handling**:
- If `cmdSleep` execution fails → fall back to `Stop()` and log warning
- If `cmdSleep` times out → fall back to `Stop()`
- If already sleeping → return success (idempotent)
- Process PID must remain unchanged after sleep

#### Wake Process

**New Method**: `func (p *Process) Wake() error`

1. Check if `cmdWake` is defined (required if `cmdSleep` is defined)
2. Validate current state is `StateSleeping`
3. Swap state to `StateWaking`
4. Execute `cmdWake` using `exec.Command()`:
   - Parse and sanitize command
   - Apply macro substitution
   - Execute with timeout (default: 60 seconds, configurable)
   - Capture stdout/stderr to processLogger
5. Check exit code:
   - Exit code 0 → proceed to health check
   - Non-zero → transition to `StateStopping`, then restart with `start()`
6. Run health check (reuse existing `checkHealthEndpoint` logic)
7. Swap state to `StateReady`
8. Emit `ProcessWakeEvent`

**Error Handling**:
- If `cmdWake` execution fails → restart process with `start()`
- If `cmdWake` times out → restart process
- If health check fails after wake → restart process
- Track wake failure count for monitoring
- On successful wake, PID must remain unchanged

#### Modified Start Logic

Update `func (p *Process) start()` to handle sleeping state:

```go
currentState := p.CurrentState()

if currentState == StateStopped {
    // Normal start flow (existing code)
    // execute cmd, health check, transition to Ready

} else if currentState == StateSleeping {
    // Wake from sleep instead of starting new process
    return p.Wake()

} else {
    return fmt.Errorf("cannot start from state %s", currentState)
}
```

### 4. Process Group Integration

**Modify `ProcessGroup.swap()` logic**:

Current behavior:
```go
if currentProcess != nil {
    currentProcess.Stop()
}
newProcess.start()
```

New behavior:
```go
if currentProcess != nil {
    if currentProcess.config.CmdSleep != "" {
        // Sleep mode enabled - use Sleep() instead of Stop()
        currentProcess.Sleep()
    } else {
        // Traditional stop mode
        currentProcess.Stop()
    }
}

// Check if new process is sleeping, wake it; otherwise start normally
if newProcess.CurrentState() == StateSleeping {
    newProcess.Wake()
} else {
    newProcess.start()
}
```

### 5. TTL Integration

**Modify TTL goroutine** in `process.go:335-358`:

Current: Calls `p.Stop()` when TTL expires

New: Check if `cmdSleep` is defined:
```go
if time.Since(p.getLastRequestHandled()) > maxDuration {
    if p.config.CmdSleep != "" {
        p.proxyLogger.Infof("<%s> Sleeping model, TTL of %ds reached", p.ID, p.config.UnloadAfter)
        p.Sleep()
    } else {
        p.proxyLogger.Infof("<%s> Unloading model, TTL of %ds reached", p.ID, p.config.UnloadAfter)
        p.Stop()
    }
    return
}
```

### 6. API Endpoint Changes

**New endpoint**: `POST /models/sleep/:model_id`
- Manually put a specific model to sleep
- Returns error if `cmdSleep` not defined for that model
- Returns 404 if model doesn't exist
- Returns 400 if model not in Ready state

**Modify**: `POST /models/unload`
- Respect sleep configuration
- Sleep instead of stop if `cmdSleep` is defined
- Otherwise use traditional Stop()

**Modify**: `GET /running`
- Include sleep status in response
- Example: `{"model": "vllm-model", "state": "sleeping"}`
- Show state for all models (stopped, starting, ready, sleeping, waking, stopping)

### 7. Events

**New Events**:
- `ProcessSleepEvent`: Emitted when process transitions to sleeping
- `ProcessWakeEvent`: Emitted when process wakes from sleep
- `ProcessWakeFailedEvent`: Emitted when wake fails

### 8. Monitoring & Logging

**Log Messages**:
- `"<%s> Executing cmdSleep"` when sleep starts
- `"<%s> Model sleep completed in %v"` with timing
- `"<%s> Executing cmdWake"` when wake starts
- `"<%s> Model wake completed in %v"` with timing
- `"<%s> cmdSleep failed (exit code %d), falling back to Stop(): %v"` on errors
- `"<%s> cmdWake failed (exit code %d), restarting process: %v"` on wake failures
- `"<%s> cmdSleep stdout: %s"` for command output (debug level)
- `"<%s> cmdWake stdout: %s"` for command output (debug level)

**Metrics** (captured in events):
- Track sleep/wake counts per model
- Track sleep/wake latencies
- Track wake failure counts
- Track fallback to Stop() counts

## Testing Plan

### Unit Tests

1. **State Transition Tests**:
   - Test valid sleep state transitions (Ready → Sleeping)
   - Test valid wake state transitions (Sleeping → Waking → Ready)
   - Test invalid transitions are rejected
   - Test fallback to stop when sleep fails

2. **Configuration Tests**:
   - Test `cmdSleep` and `cmdWake` parsing
   - Test macro substitution in commands (${PORT}, ${MODEL_ID}, ${PID})
   - Test multi-line command support with comments
   - Test validation: `cmdWake` required if `cmdSleep` defined
   - Test empty/missing cmdSleep falls back to traditional mode

3. **Command Execution Tests**:
   - Mock command execution for sleep/wake
   - Test successful sleep/wake cycle (exit code 0)
   - Test command failure handling (non-zero exit codes)
   - Test command timeout handling
   - Test concurrent sleep/wake calls
   - Test stdout/stderr capture to logger

### Integration Tests

1. **Sleep/Wake Cycle Test**:
   - Start model, verify Ready
   - Sleep model, verify Sleeping
   - Wake model, verify Ready
   - Verify process PID remains same throughout

2. **Model Swap Test**:
   - Load model A (sleep-enabled)
   - Load model B (sleep-enabled)
   - Verify model A is sleeping
   - Load model A again
   - Verify model A wakes from sleep (not restarted)
   - Measure wake time (should be <10s)

3. **TTL Integration Test**:
   - Configure model with TTL and sleep enabled
   - Make request, verify Ready
   - Wait for TTL to expire
   - Verify model transitions to Sleeping (not Stopped)
   - Make another request
   - Verify model wakes from sleep

4. **Fallback Test**:
   - Configure sleep with command that fails (e.g., `false` or invalid curl)
   - Attempt to sleep model
   - Verify fallback to Stop() is called
   - Verify model terminates properly

5. **Mixed Mode Test**:
   - Model A: sleep-enabled (has `cmdSleep`)
   - Model B: sleep-disabled (no `cmdSleep`)
   - Swap between models
   - Verify A sleeps/wakes, B stops/starts

### Manual Testing with Real Inference Servers

1. **vLLM Setup**:
   - Deploy vLLM with VLLM_SERVER_DEV_MODE=1
   - Configure llama-swap with `cmdSleep`/`cmdWake` using curl
   - Test full sleep/wake cycle
   - Measure actual wake times
   - Test both sleep levels (1 and 2) by varying cmdSleep
   - Verify process PID remains same

2. **Script-based Sleep**:
   - Create custom sleep/wake scripts
   - Configure model with `cmdSleep` and `cmdWake` pointing to scripts
   - Test that scripts receive correct macro values
   - Test error handling in scripts

3. **Error Scenarios**:
   - Stop server process while sleeping (kill PID)
   - Call sleep when already sleeping (idempotency)
   - Command timeout (slow curl response)
   - Network issues during wake
   - Invalid command syntax in config

## Implementation Checklist

### Phase 1: Core Infrastructure
- [x] Add `CmdSleep` field to `ModelConfig` struct in `proxy/config/model_config.go`
- [x] Add `CmdWake` field to `ModelConfig` struct
- [x] Add config parsing and validation (cmdWake required if cmdSleep defined)
- [x] Add macro substitution support for cmdSleep/cmdWake (reuse existing logic)
- [x] Add `StateSleeping` and `StateWaking` to `ProcessState` enum in `proxy/process.go`
- [x] Update `isValidTransition()` to support new states
- [ ] Add unit tests for state transitions

### Phase 2: Sleep/Wake Methods
- [x] Implement `Process.Sleep()` method
  - [x] Execute `cmdSleep` using `exec.Command()`
  - [x] Apply command sanitization and macro substitution
  - [x] Handle exit codes and timeouts
  - [x] Capture stdout/stderr to processLogger
  - [x] Fallback to `Stop()` on failure
- [x] Implement `Process.Wake()` method
  - [x] Execute `cmdWake` using `exec.Command()`
  - [x] Apply command sanitization and macro substitution
  - [x] Run health check after wake
  - [x] Restart process if wake fails
- [x] Update `Process.start()` to check for sleeping state and call `Wake()`
- [ ] Add unit tests with mocked command execution

### Phase 3: Integration Points
- [x] Modify `ProcessGroup.swap()` to check `cmdSleep` and call `Sleep()` when defined
- [x] Update TTL goroutine to check `cmdSleep` and call `Sleep()` instead of `Stop()`
- [x] Ensure process isn't terminated when sleeping (PID must remain same)
- [ ] Add integration tests for swap with sleep
- [ ] Test that sleeping processes don't get killed during swap

### Phase 4: Events & Monitoring
- [x] Define `ProcessSleepEvent` and `ProcessWakeEvent` (using existing ProcessStateChangeEvent with new states)
- [x] Emit events at appropriate lifecycle points
- [x] Add detailed logging for sleep/wake operations
- [x] Add timing metrics for sleep/wake operations

### Phase 5: API Endpoints
- [ ] Add `POST /models/sleep/:model_id` endpoint
- [ ] Modify `POST /models/unload` to respect sleep config
- [ ] Modify `GET /running` to show sleep status
- [ ] Add API tests

### Phase 6: Documentation
- [ ] Update configuration.md with sleep mode section
  - [ ] Document `cmdSleep` configuration option
  - [ ] Document `cmdWake` configuration option
  - [ ] Document macro support in sleep/wake commands
  - [ ] Document fallback behavior
- [ ] Create sleep-wake-mode.md example in docs/examples/
  - [ ] vLLM sleep mode example with curl commands
  - [ ] Custom script example
  - [ ] Docker container example
  - [ ] Future llama.cpp example (hypothetical)
- [ ] Document performance expectations (based on vLLM benchmarks)
- [ ] Update README.md features list with sleep/wake support
- [ ] Update config.example.yaml with cmdSleep/cmdWake examples

### Phase 7: Testing
- [x] Run `make test-dev` and fix any issues (no new issues, TestProxyManager_StartupHooks was already broken)
- [ ] Run `make test-all` for concurrency tests
- [ ] Perform manual testing with real vLLM instance
- [ ] Test both sleep level 1 and level 2
- [ ] Test fallback scenarios
- [ ] Performance testing (measure actual wake times)

## Known Limitations & Future Enhancements

### Current Limitations
1. Sleep mode requires manual configuration (`cmdSleep` and `cmdWake`)
2. No automatic detection of server sleep capability
3. Wake failures result in full process restart
4. Fixed timeouts for sleep (30s) and wake (60s) commands
5. No built-in validation that server actually supports sleep (relies on command exit codes)
6. Process must remain running during sleep (process termination breaks sleep mode)

### Future Enhancements
1. **Configurable timeouts**: Add `cmdSleepTimeout` and `cmdWakeTimeout` options
2. **Auto-detection**: Probe server capabilities and auto-configure sleep commands
3. **Preemptive wake**: Start waking process before request arrives (predictive loading)
4. **Retry logic**: Configurable retry attempts for wake failures before full restart
5. **Health check during sleep**: Periodic checks to verify sleeping process is still alive
6. **Metrics dashboard**: UI showing sleep/wake statistics, timing, and failure rates
7. **Command templating**: Pre-built templates for common servers (vLLM, llama.cpp, etc.)
8. **Graceful degradation**: Automatically disable sleep mode if repeated failures occur
9. **Sleep depth control**: Allow dynamic sleep level selection based on memory pressure
10. **State persistence**: Remember sleep state across llama-swap restarts (if process still running)

## References

- vLLM Sleep Mode Docs: https://docs.vllm.ai/en/stable/features/sleep_mode.html
- vLLM Blog Post: https://blog.vllm.ai/2025/10/26/sleep-mode.html
- llama-swap Process Lifecycle: `proxy/process.go`
- llama-swap Configuration: `proxy/config/model_config.go`
