# Unify start() and wake() Structure with UnloadAfter Support

## Overview

The `start()` and `wake()` functions in `proxy/process.go` both accomplish similar goals (transitioning a process to `StateReady`), but are implemented with different structures and behaviors. This inconsistency makes it difficult to add new functionality to both functions and creates a maintenance burden.

**Key Issue**: The `wake()` function lacks the `UnloadAfter` TTL monitoring behavior that `start()` implements. When a model wakes from sleep, it should have the same automatic unload behavior as when it starts fresh.

**User Requirement**: The UnloadAfter behavior should always call `Stop()` (not `Sleep()`), regardless of whether the process was started or woken.

## ⚠️ Important Behavioral Changes

This refactor includes **intentional behavioral changes** to `wake()`:

1. **Error Recovery Change** (Breaking Change):
   - **Old behavior**: When `wake()` fails (executeWakeCommand or health check), it automatically restarts the process via `start()`
   - **New behavior**: `wake()` will fail fast - return error and transition to Stopped state
   - **Impact**:
     - Direct callers: `wake()` is private and only called by `start()` (line 257)
     - `start()` already returns `wake()`'s error to its caller (`ProxyRequest`)
     - This change prevents potential recursive wake→start→wake loops
     - Error propagation path remains the same: wake() → start() → ProxyRequest
   - **Rationale**: Consistent with `start()`, simpler error handling, fewer failure points, prevents recursion

2. **UnloadAfter Addition** (New Feature):
   - **Old behavior**: `wake()` does not start UnloadAfter monitoring
   - **New behavior**: `wake()` starts UnloadAfter monitoring after reaching Ready
   - **Impact**: Models woken from sleep will auto-unload after TTL, just like started models
   - **Rationale**: Consistent behavior regardless of how process reached Ready state

## Current State Analysis

### start() Function Structure (lines 247-401)
1. Validates proxy configuration
2. Checks if sleeping/asleep → redirects to `wake()`
3. Gets sanitized command arguments
4. State transition: `StateStopped` → `StateStarting`
5. Handles concurrent starts via `waitStarting.Wait()`
6. Creates command context with cancel function
7. Sets up exec.Cmd with logging, env, cancel callback
8. Tracks `failedStartCount`
9. Starts the command process
10. Spawns goroutine for `waitForCmd()`
11. Waits 250ms for process startup
12. Runs health check loop with timeout
13. **Starts UnloadAfter TTL monitoring goroutine** (lines 370-393)
14. State transition: `StateStarting` → `StateReady`
15. Resets `failedStartCount` to 0

### wake() Function Structure (lines 594-688)
1. Gets current state
2. Handles concurrent wakes via `waitWaking.Wait()`
3. Validates state (must be `StateAsleep` or `StateSleepPending`)
4. Handles edge case: waking from `StateSleepPending`
5. State transition: `StateAsleep` → `StateWaking`
6. Executes wake command via `executeWakeCommand()`
7. On wake command failure → stop and restart via `start()`
8. Runs health check loop with timeout
9. On health check failure → stop and restart via `start()`
10. State transition: `StateWaking` → `StateReady`
11. **Missing: UnloadAfter TTL monitoring** ⚠️

### Key Differences

| Aspect | start() | wake() | Issue |
|--------|---------|--------|-------|
| **UnloadAfter monitoring** | ✅ Implemented | ❌ Missing | Wake doesn't auto-unload |
| **Concurrent call handling** | waitStarting | waitWaking | Inconsistent naming but similar |
| **Error recovery** | Transition to Stopped | Stop then start() | Different approaches |
| **Command execution** | Creates new exec.Cmd | Executes wake command | Expected difference |
| **Failed attempt tracking** | failedStartCount | None | Wake doesn't track failures |
| **Health check** | ✅ Implemented | ✅ Implemented | Both have it |

### UnloadAfter Behavior Analysis

The UnloadAfter goroutine in `start()` (lines 370-393):
```go
if p.config.UnloadAfter > 0 {
    go func() {
        maxDuration := time.Duration(p.config.UnloadAfter) * time.Second

        for range time.Tick(time.Second) {
            if p.CurrentState() != StateReady {
                return
            }

            // skip the TTL check if there are inflight requests
            if p.inFlightRequestsCount.Load() != 0 {
                continue
            }

            if time.Since(p.getLastRequestHandled()) > maxDuration {
                p.proxyLogger.Infof("<%s> Unloading model, TTL of %ds reached", p.ID, p.config.UnloadAfter)
                p.Stop()  // ← Always calls Stop(), never Sleep()
                return
            }
        }
    }()
}
```

This behavior:
- Monitors idle time after last request
- Only triggers when no inflight requests exist
- Calls `Stop()` to fully unload the model
- Should apply equally to woken and started processes

## Code Style Inconsistencies

Beyond the functional differences, `start()` and `wake()` (and `Sleep()`) implement similar patterns in very different ways. This makes the code harder to understand, maintain, and extend.

### 1. Concurrent Call Handling

**start() approach** (lines 265-282):
```go
if curState, err := p.swapState(StateStopped, StateStarting); err != nil {
    if err == ErrExpectedStateMismatch {
        // Embedded inside swapState error handling
        if curState == StateStarting {
            p.waitStarting.Wait()
            if state := p.CurrentState(); state == StateReady {
                return nil
            } else {
                return fmt.Errorf("process was already starting but wound up in state %v", state)
            }
        } else {
            return fmt.Errorf("processes was in state %v when start() was called", curState)
        }
    } else {
        return fmt.Errorf("failed to set Process state to starting: current state: %v, error: %v", curState, err)
    }
}
```

**wake() and Sleep() approach** (lines 597-607, 493-503):
```go
// Handle concurrent Wake() calls - BEFORE swapState
if currentState == StateWaking {
    p.proxyLogger.Debugf("<%s> Wake already in progress, waiting for completion", p.ID)
    p.waitWaking.Wait()
    if state := p.CurrentState(); state == StateReady {
        p.proxyLogger.Debugf("<%s> Wake completed by concurrent call", p.ID)
        return nil
    } else {
        return fmt.Errorf("wake operation failed, state: %v", state)
    }
}
```

**Issue**:
- `start()` embeds concurrent call handling **inside** the swapState error handler
- `wake()` and `Sleep()` check for transitional state **before** attempting swapState
- The wake/Sleep pattern is clearer and more explicit
- **Recommendation**: Prefer the wake/Sleep pattern - check transitional state upfront

### 2. State Validation

**start() approach**: No explicit upfront validation
- Relies on `swapState()` to fail if not in `StateStopped`
- Error messages come from swapState error handling

**wake() approach** (lines 610-612):
```go
// Can only wake from asleep or sleep pending states
if currentState != StateAsleep && currentState != StateSleepPending {
    return fmt.Errorf("cannot wake from state %s, must be asleep or sleep pending", currentState)
}
```

**Sleep() approach** (line 512):
```go
// Can only sleep from Ready state
if !isValidTransition(currentState, StateSleepPending) {
    p.proxyLogger.Warnf("<%s> Cannot sleep from state %s", p.ID, currentState)
    return
}
```

**Issue**:
- Three different validation patterns
- `Sleep()` uses `isValidTransition()` helper
- `wake()` has explicit state checks
- `start()` has no explicit validation
- **Recommendation**: Use explicit validation with clear error messages like wake() does

### 3. Health Check Loop Structure

**start() approach** (lines 341-367):
```go
for {
    currentState := p.CurrentState()
    if currentState != StateStarting {
        if currentState == StateStopped {
            return fmt.Errorf("upstream command exited prematurely but successfully")
        }
        return errors.New("health check interrupted due to shutdown")
    }

    if time.Since(checkStartTime) > maxDuration {
        p.stopCommand()
        return fmt.Errorf("health check timed out after %vs", maxDuration.Seconds())
    }

    if err := p.checkHealthEndpoint(healthURL); err == nil {
        p.proxyLogger.Infof("<%s> Health check passed on %s", p.ID, healthURL)
        break
    } else {
        if strings.Contains(err.Error(), "connection refused") {
            ttl := time.Until(checkStartTime.Add(maxDuration))
            p.proxyLogger.Debugf("<%s> Connection refused on %s, giving up in %.0fs (normal during startup)", p.ID, healthURL, ttl.Seconds())
        } else {
            p.proxyLogger.Debugf("<%s> Health check error on %s, %v (normal during startup)", p.ID, healthURL, err)
        }
    }
    <-time.After(p.healthCheckLoopInterval)
}
```

**wake() approach** (lines 660-679):
```go
for {
    if err := p.checkHealthEndpoint(healthURL); err == nil {
        break // Health check passed
    }

    if time.Since(healthCheckStart) > timeout {
        p.proxyLogger.Errorf("<%s> Health check failed after wake, restarting process", p.ID)
        // Transition to stopping, then restart
        if _, swapErr := p.swapState(StateWaking, StateStopping); swapErr != nil {
            p.proxyLogger.Errorf("<%s> Failed to transition from waking to stopping: %v", p.ID, swapErr)
        }
        p.stopCommand()
        if _, swapErr := p.swapState(StateStopping, StateStopped); swapErr != nil {
            p.proxyLogger.Errorf("<%s> Failed to transition to stopped: %v", p.ID, swapErr)
        }
        return p.start()
    }

    time.Sleep(p.healthCheckLoopInterval)
}
```

**Differences**:
- `start()` checks current state inside loop, wake() doesn't
- `start()` has more detailed logging for connection errors
- `start()` calls `stopCommand()` on timeout, wake() does multiple state transitions + restart
- `start()` uses `<-time.After()`, wake() uses `time.Sleep()`
- `start()` logs at Info level on success, wake() doesn't log success

**Issues**:
- Different timeout handling strategies (stop vs restart)
- Inconsistent sleep/wait mechanisms
- Different logging verbosity
- **Recommendation**: Standardize on `time.Sleep()` and consistent logging

### 4. Error Message Formatting

**start() error messages**:
```go
"failed to set Process state to starting: current state: %v, error: %v"
"process was already starting but wound up in state %v"
"processes was in state %v when start() was called"
```

**wake() error messages**:
```go
"failed to transition to waking: %v, current state: %v"
"wake operation failed, state: %v"
"Failed to transition from waking to stopping: %v"
```

**Issues**:
- Inconsistent capitalization ("Failed" vs "failed")
- Inconsistent parameter order (error first vs state first)
- Different terminology ("set Process state" vs "transition")
- **Recommendation**: Standardize on lowercase, consistent order, use "transition" terminology

### 5. Error Recovery Strategies

**start() on command start failure** (lines 305-315):
```go
if err != nil {
    if curState, swapErr := p.swapState(StateStarting, StateStopped); swapErr != nil {
        p.forceState(StateStopped) // force it into a stopped state
        return fmt.Errorf(...)
    }
    return fmt.Errorf(...)
}
```

**wake() on command execution failure** (lines 639-651):
```go
if err := p.executeWakeCommand(); err != nil {
    p.proxyLogger.Errorf("<%s> cmdWake failed, restarting process: %v", p.ID, err)
    // Transition to stopping, then restart
    if _, swapErr := p.swapState(StateWaking, StateStopping); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition from waking to stopping: %v", p.ID, swapErr)
    }
    p.stopCommand()
    // Wait for stop to complete, then start fresh
    if _, swapErr := p.swapState(StateStopping, StateStopped); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition to stopped: %v", p.ID, swapErr)
    }
    return p.start()
}
```

**Differences**:
- `start()` does a single state transition and returns error (fail fast)
- `wake()` does multiple state transitions, stops command, and restarts via `start()` (complex recovery)
- Different philosophies: fail fast vs retry

**Issue**:
- Very different error recovery strategies make code harder to understand and maintain
- Complex recovery in wake() adds more failure points and state transition complexity
- Both should fail fast and let the caller decide whether to retry
- **Recommendation**: Align wake() with start() - use fail fast strategy for consistency

### 6. Logging Patterns

**Inconsistencies**:
- `start()` logs "Executing start command" at Debug level (line 302)
- `wake()` logs "Executing cmdWake" at Info level via `executeWakeCommand()` (line 704)
- Health check success: start() logs at Info, wake() doesn't log
- Error logging: wake() is more verbose with multiple Errorf calls
- **Recommendation**: Standardize logging levels for similar operations

### 7. Time Waiting Mechanisms

- `start()` uses `<-time.After(p.healthCheckLoopInterval)` (line 366)
- `wake()` uses `time.Sleep(p.healthCheckLoopInterval)` (line 678)
- `Sleep()` uses `time.Sleep()` (in executeSleepCommand)

**Issue**:
- Both mechanisms work but are stylistically different
- `time.Sleep()` is simpler and more direct
- **Recommendation**: Standardize on `time.Sleep()` for consistency

## Code Style Recommendations

Based on the analysis above, we should establish these patterns:

### Pattern 1: Concurrent Call Handling (use wake/Sleep pattern)
```go
// Check for transitional state BEFORE swapState
currentState := p.CurrentState()
if currentState == StateTransitional {
    p.waitGroup.Wait()
    if state := p.CurrentState(); state == StateTarget {
        return nil
    } else {
        return fmt.Errorf("operation failed, state: %v", state)
    }
}
```

### Pattern 2: Explicit State Validation
```go
// Validate state BEFORE attempting swapState
if !canTransitionFrom(currentState) {
    return fmt.Errorf("cannot transition from state %s", currentState)
}
```

### Pattern 3: Consistent Error Messages
```go
// Format: lowercase, state first, error second
"failed to transition to <target>: current state: %v, error: %v"
```

### Pattern 4: Consistent Time Waiting
```go
// Use time.Sleep() for consistency
time.Sleep(duration)
```

### Pattern 5: Consistent Logging Levels
- Debug: State transitions, detailed flow info
- Info: Command execution, significant events, health check success
- Warn: Unexpected but recoverable conditions
- Error: Failures requiring recovery or user attention

## Design Requirements

### 1. Extract Common Post-Ready Logic

**Goal**: Create a shared method that both `start()` and `wake()` call after reaching `StateReady`.

**Method signature**:
```go
// startUnloadMonitoring begins TTL monitoring for automatic model unloading.
// This should be called after the process reaches StateReady.
func (p *Process) startUnloadMonitoring()
```

**Implementation details**:
- Extract lines 370-393 from `start()` into this new method
- Check `p.config.UnloadAfter > 0` before starting goroutine
- Maintain exact same behavior: monitor idle time, call `Stop()` on timeout
- Add debug logging when monitoring starts

### 2. Refactor wake() to Call startUnloadMonitoring()

**Changes required**:
1. After successful state transition to `StateReady` (line 682)
2. Before returning from `wake()` function
3. Add call: `p.startUnloadMonitoring()`

**Location**: Between lines 684-687, after the swapState call succeeds

### 3. Update start() to Call startUnloadMonitoring()

**Changes required**:
1. Replace inline UnloadAfter goroutine (lines 370-393) with method call
2. Call after health check passes, before final state transition
3. Maintain same timing/position in execution flow

**Location**: Replace lines 370-393 with single call to `p.startUnloadMonitoring()`

### 4. Align Code Styles Between start() and wake()

While the primary goal is UnloadAfter support, we should also address the code style inconsistencies identified above to improve maintainability.

#### 4.1 Standardize Concurrent Call Handling in start()

**Current**: start() handles concurrency inside swapState error handler (lines 265-282)
**Target**: Move to upfront check pattern like wake() and Sleep()
**Priority**: HIGH - improves code clarity

**Refactor** (in start()):
```go
// Before attempting swapState, check if already starting
currentState := p.CurrentState()
if currentState == StateStarting {
    p.proxyLogger.Debugf("<%s> Start already in progress, waiting for completion", p.ID)
    p.waitStarting.Wait()
    if state := p.CurrentState(); state == StateReady {
        p.proxyLogger.Debugf("<%s> Start completed by concurrent call", p.ID)
        return nil
    } else {
        return fmt.Errorf("start operation failed, state: %v", state)
    }
}

// Can only start from stopped state
if currentState != StateStopped {
    return fmt.Errorf("cannot start from state %s, must be stopped", currentState)
}

// Now attempt the state transition
if curState, err := p.swapState(StateStopped, StateStarting); err != nil {
    return fmt.Errorf("failed to transition to starting: current state: %v, error: %v", curState, err)
}
```

#### 4.2 Standardize Error Message Format

**Apply to both start() and wake()**:
- Use lowercase for all error messages
- Use "transition" terminology consistently
- Order: "failed to transition to X: current state: %v, error: %v"
- Use consistent logging patterns

**Examples**:
```go
// Before (start):
"failed to set Process state to starting: current state: %v, error: %v"

// After (start):
"failed to transition to starting: current state: %v, error: %v"

// Before (wake - inconsistent capitalization):
"Failed to transition from waking to stopping: %v"

// After (wake):
"failed to transition from waking to stopping: %v"
```

#### 4.3 Standardize Time Waiting

**Change in start()** (line 366):
```go
// Before:
<-time.After(p.healthCheckLoopInterval)

// After:
time.Sleep(p.healthCheckLoopInterval)
```

**Rationale**: Consistency with wake() and Sleep(), simpler syntax

#### 4.4 Enhance Health Check Logging Consistency

**In wake()**: Add health check success logging like start() does
```go
if err := p.checkHealthEndpoint(healthURL); err == nil {
    p.proxyLogger.Infof("<%s> Health check passed on %s", p.ID, healthURL)
    break
}
```

**Optional**: Consider adding connection-specific error logging in wake() like start() has

#### 4.5 Align Error Recovery Strategy - Fail Fast

**Current wake() behavior**: Complex recovery with multiple state transitions and restart via `start()`
**Target behavior**: Match start() - fail fast with single state transition

**Refactor wake() error handling**:

**On executeWakeCommand() failure** (lines 639-651):
```go
// Before:
if err := p.executeWakeCommand(); err != nil {
    p.proxyLogger.Errorf("<%s> cmdWake failed, restarting process: %v", p.ID, err)
    // Transition to stopping, then restart
    if _, swapErr := p.swapState(StateWaking, StateStopping); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition from waking to stopping: %v", p.ID, swapErr)
    }
    p.stopCommand()
    if _, swapErr := p.swapState(StateStopping, StateStopped); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition to stopped: %v", p.ID, swapErr)
    }
    return p.start()
}

// After:
if err := p.executeWakeCommand(); err != nil {
    p.proxyLogger.Errorf("<%s> cmdWake failed: %v", p.ID, err)
    if curState, swapErr := p.swapState(StateWaking, StateStopped); swapErr != nil {
        p.forceState(StateStopped)
        return fmt.Errorf("failed to execute wake command '%s' and state swap failed: wake error: %v, current state: %v, state swap error: %v",
            p.config.CmdWake, err, curState, swapErr)
    }
    return fmt.Errorf("wake command failed: %v", err)
}
```

**On health check timeout failure** (lines 665-676):
```go
// Before:
if time.Since(healthCheckStart) > timeout {
    p.proxyLogger.Errorf("<%s> Health check failed after wake, restarting process", p.ID)
    // Transition to stopping, then restart
    if _, swapErr := p.swapState(StateWaking, StateStopping); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition from waking to stopping: %v", p.ID, swapErr)
    }
    p.stopCommand()
    if _, swapErr := p.swapState(StateStopping, StateStopped); swapErr != nil {
        p.proxyLogger.Errorf("<%s> Failed to transition to stopped: %v", p.ID, swapErr)
    }
    return p.start()
}

// After:
if time.Since(healthCheckStart) > timeout {
    p.proxyLogger.Errorf("<%s> Health check timed out after wake", p.ID)
    p.stopCommand()
    if curState, swapErr := p.swapState(StateWaking, StateStopped); swapErr != nil {
        p.forceState(StateStopped)
        return fmt.Errorf("health check timed out after wake and state swap failed: current state: %v, state swap error: %v", curState, swapErr)
    }
    return fmt.Errorf("health check timed out after wake (timeout: %v)", timeout)
}
```

**Rationale**:
- Fail fast pattern is simpler and has fewer failure points
- Reduces state transition complexity
- Caller (e.g., ProxyRequest via start()) can decide whether to retry
- Consistent error handling strategy across start() and wake()
- Makes debugging easier with clearer error messages

### 5. Ensure UnloadAfter Always Calls Stop()

**Verification**:
- Confirm that `startUnloadMonitoring()` always calls `p.Stop()`
- Never calls `p.Sleep()` - this is critical per user requirement
- Document this behavior clearly in code comments

**Rationale**:
- UnloadAfter is a TTL for keeping model loaded in memory
- After timeout, the model should be fully unloaded, not just slept
- Sleep is for immediate idle detection, UnloadAfter is for maximum load time

## Testing Plan

### Unit Tests

1. **Test startUnloadMonitoring() method**
   - File: `proxy/process_test.go`
   - Test that monitoring starts when `UnloadAfter > 0`
   - Test that monitoring doesn't start when `UnloadAfter = 0`
   - Test that `Stop()` is called after timeout
   - Test that monitoring respects inflight requests
   - Test that monitoring exits when state changes from Ready

2. **Test wake() with UnloadAfter**
   - File: `proxy/process_test.go`
   - Test that wake() starts UnloadAfter monitoring
   - Test that woken process auto-unloads after TTL
   - Test that UnloadAfter timeout is respected
   - Test that process calls `Stop()` not `Sleep()`

3. **Test start() with refactored UnloadAfter**
   - Verify existing tests still pass
   - Confirm no behavioral changes
   - Test that start() still auto-unloads after TTL

### Integration Tests

1. **Test wake → idle → auto-unload flow**
   - Start process, let it reach Ready
   - Call Sleep(), verify StateAsleep
   - Make request to wake process
   - Wait for UnloadAfter timeout without requests
   - Verify process transitions to Stopped (not Asleep)

2. **Test start → idle → auto-unload flow**
   - Start process fresh
   - Wait for UnloadAfter timeout without requests
   - Verify process transitions to Stopped
   - Verify behavior unchanged from before refactor

3. **Test UnloadAfter with inflight requests**
   - Wake process from sleep
   - Start long-running request
   - Wait past UnloadAfter timeout
   - Verify process stays Ready during request
   - Complete request
   - Verify process stops after timeout post-request

4. **Test wake() error recovery behavioral change** ⚠️ IMPORTANT
   - **Old behavior**: wake() would restart process via start() on failure
   - **New behavior**: wake() returns error and transitions to Stopped
   - Test scenarios:
     - executeWakeCommand() fails → verify returns error, state is Stopped
     - Health check timeout → verify returns error, state is Stopped
     - Verify no automatic restart happens
     - Verify caller (start()) receives error and can handle retry if needed

### Test Coverage Goals

- Ensure `startUnloadMonitoring()` has 100% line coverage
- Maintain or improve existing test coverage for `start()` and `wake()`
- Add edge case tests for concurrent wake + UnloadAfter scenarios

## Implementation Checklist

### Phase 1: Extract Common Logic
- [ ] Create `startUnloadMonitoring()` method in `proxy/process.go`
- [ ] Extract UnloadAfter goroutine logic from `start()` (lines 370-393)
- [ ] Add documentation explaining behavior and Stop() vs Sleep() distinction
- [ ] Add debug log message when monitoring starts

### Phase 2: Align Code Style in start() Function
- [ ] Move concurrent call handling to upfront check (before swapState)
- [ ] Add explicit state validation before swapState
- [ ] Standardize error messages to use "transition" terminology
- [ ] Change `<-time.After()` to `time.Sleep()` in health check loop
- [ ] Update all error messages to lowercase format
- [ ] Add explanatory comments for error recovery strategy
- [ ] Replace inline UnloadAfter code (lines 370-393) with call to `startUnloadMonitoring()`
- [ ] Verify call happens after health check passes, before state transition to Ready
- [ ] Run existing tests to ensure no behavioral changes

### Phase 3: Align Code Style in wake() Function
- [ ] Standardize error message capitalization (lowercase)
- [ ] Ensure consistent error message parameter ordering
- [ ] Add health check success logging (Info level)
- [ ] **Refactor error recovery to fail fast (match start() pattern)**:
  - [ ] Replace complex recovery in executeWakeCommand() error handler (lines 639-651)
  - [ ] Replace complex recovery in health check timeout handler (lines 665-676)
  - [ ] Use single state transition (StateWaking → StateStopped)
  - [ ] Remove automatic restart via start() - let caller decide
  - [ ] Add forceState() fallback if swapState fails
  - [ ] Update error messages to be consistent with start()
- [ ] Add call to `startUnloadMonitoring()` after successful state transition to Ready
- [ ] Position call between line 684-687 (after swapState succeeds, before return)
- [ ] Add comment explaining why UnloadAfter is needed

### Phase 4: Testing
- [ ] Write unit tests for `startUnloadMonitoring()` method
- [ ] Write integration test for wake + UnloadAfter flow
- [ ] **Write tests for wake() error recovery changes**:
  - [ ] Test that wake() returns error (doesn't restart) when executeWakeCommand() fails
  - [ ] Test that wake() returns error (doesn't restart) when health check times out
  - [ ] Test that wake() transitions to Stopped state on failure
  - [ ] Test that wake() uses forceState() if swapState fails during error recovery
  - [ ] Verify error messages match expected format
- [ ] Run `make test-dev` to verify no regressions
- [ ] Run `make test-all` for full test suite including concurrency tests
- [ ] Fix any staticcheck warnings or errors

### Phase 5: Validation
- [ ] Review that UnloadAfter always calls Stop(), never Sleep()
- [ ] Verify both start() and wake() have identical post-Ready behavior
- [ ] Confirm no behavioral changes to existing UnloadAfter functionality
- [ ] Test with manual scenarios (start → idle → unload, wake → idle → unload)

### Phase 6: Documentation
- [ ] Add inline comments explaining the unified behavior
- [ ] Update any relevant documentation about UnloadAfter behavior
- [ ] Document that UnloadAfter applies to both started and woken processes

## Success Criteria

1. ✅ **Functional Parity**: Both `start()` and `wake()` call `startUnloadMonitoring()` after reaching Ready
2. ✅ **UnloadAfter Behavior**: Wake function has automatic unload after TTL expires
3. ✅ **Stop vs Sleep**: UnloadAfter always calls `Stop()`, never `Sleep()`
4. ✅ **Code Maintainability**: Single source of truth for UnloadAfter logic
5. ✅ **Code Style Consistency**: Both functions use consistent patterns for:
   - Concurrent call handling (upfront state check pattern)
   - State validation (explicit validation before swapState)
   - Error message formatting (lowercase, "transition" terminology)
   - Time waiting mechanism (time.Sleep)
   - Logging patterns (consistent levels and verbosity)
   - **Error recovery strategy (fail fast pattern)**
6. ✅ **Test Coverage**: New behavior fully tested with passing tests
7. ✅ **Behavioral Change Managed**: wake() error recovery change is intentional and tested
8. ✅ **Static Analysis**: No new staticcheck warnings or errors

## Notes

- The extraction of `startUnloadMonitoring()` addresses the functional gap (UnloadAfter missing in wake)
- Code style alignment improves maintainability and makes future enhancements easier
- This refactor combines functional fixes with style improvements in a single effort
- The distinction between Stop() and Sleep() is intentional and must be preserved
- **Error recovery alignment**: wake() will now fail fast like start() instead of automatic retry
  - This is a **behavioral change** to wake() - it will now return errors instead of restarting
  - Callers (e.g., start() when called from ProxyRequest) can decide whether to retry
  - Simpler error handling with fewer state transitions
  - More predictable behavior and easier debugging
- Both functions will follow consistent patterns after refactoring, making the codebase more maintainable

## References

- `proxy/process.go`: Lines 247-401 (start function)
- `proxy/process.go`: Lines 594-688 (wake function)
- `proxy/process.go`: Lines 370-393 (UnloadAfter implementation)
- Related issue: Sleep/wake state management improvements
