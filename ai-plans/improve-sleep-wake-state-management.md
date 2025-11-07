# Improve Sleep/Wake State Management

## Title
Split StateSleeping into StateSleepPending and StateAsleep following StateStarting/StateReady pattern

## Overview

The current sleep/wake implementation uses a single `StateSleeping` state, but the code has TODO comments (process.go:36 and process.go:184) indicating that it should follow the same two-state pattern as the start process uses with `StateStarting` and `StateReady`.

### Current Problem

**StateStarting/StateReady Pattern** (working correctly):
- `StateStarting`: Transitional state while `cmd` is executing and health checks run
- `StateReady`: Final state after process is fully operational
- `waitStarting` WaitGroup: Handles concurrent start() calls
  - When entering StateStarting, atomically increments waitStarting
  - Concurrent start() calls wait on waitStarting.Wait()
  - After completion, checks if StateReady was reached

**StateSleeping Pattern** (needs improvement):
- `StateSleeping`: Single state used both during and after sleep
- No distinction between "sleeping in progress" vs "fully asleep"
- No waitSleeping mechanism for concurrent Sleep() calls
- No waitWaking mechanism for concurrent Wake() calls

### Why This Matters

1. **Race conditions**: Multiple threads calling Sleep() concurrently could cause state conflicts
2. **Inconsistent patterns**: StateStarting has two states but StateSleeping has one
3. **Lack of visibility**: Can't distinguish between "cmdSleep is running" vs "model is asleep"
4. **Concurrent access**: No synchronization for multiple Sleep()/Wake() calls like start() has
5. **Testing complexity**: Harder to test intermediate vs final states

## Analysis of Current State Management

### Current States (process.go:26-39)
```
StateStopped   - Process not running
StateStarting  - Transitional: cmd executing, health check running
StateReady     - Final: Process operational and healthy
StateStopping  - Transitional: Process shutting down
StateShutdown  - Final: Terminal state, no restart allowed
StateSleeping  - Single state (PROBLEM: both transitional AND final)
StateWaking    - Transitional: cmdWake executing, health check running
```

### Current State Transition Rules (process.go:195-213)
```
StateStopped   → StateStarting
StateStarting  → StateReady, StateStopping, StateStopped
StateReady     → StateStopping, StateSleeping
StateStopping  → StateStopped, StateShutdown
StateShutdown  → (terminal, no transitions)
StateSleeping  → StateWaking, StateStopping
StateWaking    → StateReady, StateStopping
```

### Current Implementation Issues

**Sleep() method (process.go:467-511)**:
- Line 493: Swaps to StateSleeping immediately
- Line 500: Executes cmdSleep (can take time)
- Problem: StateSleeping entered before operation completes
- No waitSleeping to handle concurrent calls

**Wake() method (process.go:551-616)**:
- Line 560: Swaps to StateWaking (correct - transitional state)
- Line 567: Executes cmdWake
- Line 610: Swaps to StateReady (correct - final state)
- StateWaking follows the correct pattern!
- No waitWaking to handle concurrent calls

**start() method (process.go:233-387)**:
- Line 251: Swaps to StateStarting (transitional)
- Line 255-261: If already StateStarting, waits on waitStarting.Wait()
- Line 270: waitStarting.Add(1) called atomically in swapState()
- Line 271: defer waitStarting.Done()
- Line 381: Swaps to StateReady (final)
- This is the pattern Sleep() should follow!

## Design Requirements

### Recommended State Changes

Replace single `StateSleeping` state with two states:

1. **`StateSleepPending`** (transitional state)
   - Entered when Sleep() is called
   - cmdSleep is executing
   - Sleep operation in progress
   - Similar to StateStarting
   - Common state machine terminology for "operation not complete yet"

2. **`StateAsleep`** (final state)
   - Entered after cmdSleep completes successfully
   - Process is fully asleep and stable
   - Ready to be woken
   - Similar to StateReady

### Alternative State Names Considered

| Option | Pros | Cons | Recommendation |
|--------|------|------|----------------|
| StateSleepPending/StateAsleep | Common state machine term, clear "not done yet" meaning | Breaks from -ing suffix pattern | **CHOSEN** ⭐ |
| StateSleepStarting/StateAsleep | Directly parallels StateStarting | Slightly verbose | Second choice |
| StateSleeping/StateAsleep | Follows natural language | "Sleeping" is ambiguous - could be action or state | Not recommended |
| StateEnteringSleep/StateAsleep | Natural English, clear transition | Doesn't match existing pattern | Not recommended |
| StateSleeping/StateSlept | Past tense indicates completion | "Slept" sounds awkward | Not recommended |

**Selected: StateSleepPending (transitional) / StateAsleep (final)**

Reasoning:
- **StateSleepPending**: Common state machine terminology, unambiguous that operation is in progress
- **StateAsleep**: Clear final state, unambiguous (the model IS asleep)
- Clear distinction between "sleep pending" (command running) vs "asleep" (command completed)
- Easy to understand in logs: "sleep pending" → "asleep" → "waking" → "ready"

### Updated State Transition Rules

```
StateStopped      → StateStarting
StateStarting     → StateReady, StateStopping, StateStopped
StateReady        → StateStopping, StateSleepPending
StateStopping     → StateStopped, StateShutdown
StateShutdown     → (terminal, no transitions)

StateSleepPending → StateAsleep, StateStopping        (NEW)
StateAsleep       → StateWaking, StateStopping        (NEW)
StateWaking       → StateReady, StateStopping
```

Key changes:
- StateReady → StateSleepPending (not StateAsleep directly)
- StateSleepPending → StateAsleep (after cmdSleep succeeds)
- StateSleepPending → StateStopping (if cmdSleep fails)
- StateAsleep → StateWaking (when wake requested)
- StateAsleep → StateStopping (when explicit stop requested)

### Concurrency Synchronization

Add synchronization primitives to Process struct (process.go:48-89):

```go
// Add to Process struct:
waitSleeping  sync.WaitGroup  // Block concurrent Sleep() calls
waitWaking    sync.WaitGroup  // Block concurrent Wake() calls
```

**Pattern from waitStarting** (process.go:182-187):
- Atomically increment WaitGroup in swapState() when entering transitional state
- Concurrent calls detect the transitional state and wait
- After operation completes, check if expected final state was reached

### Updated Sleep() Implementation Pattern

**Current flow** (incorrect):
```
1. swapState(StateReady, StateSleeping)
2. Execute cmdSleep
3. If success: stay in StateSleeping
4. If failure: swapState(StateSleeping, StateStopping)
```

**New flow** (correct, following start() pattern):
```
1. swapState(StateReady, StateSleepPending)
   - Atomically increments waitSleeping in swapState()
2. defer waitSleeping.Done()
3. Execute cmdSleep
4. If success: swapState(StateSleepPending, StateAsleep)
5. If failure: swapState(StateSleepPending, StateStopping)

Concurrent Sleep() calls:
- Detect StateSleepPending state
- Call waitSleeping.Wait()
- Check if StateAsleep was reached
- If not, return error
```

### Updated Wake() Implementation Pattern

**Current flow** (mostly correct):
```
1. swapState(StateSleeping, StateWaking)
2. Execute cmdWake
3. Run health check
4. swapState(StateWaking, StateReady)
```

**New flow** (with StateAsleep and waitWaking):
```
1. swapState(StateAsleep, StateWaking)
   - Atomically increments waitWaking in swapState()
2. defer waitWaking.Done()
3. Execute cmdWake
4. Run health check
5. If success: swapState(StateWaking, StateReady)
6. If failure: restart process

Concurrent Wake() calls:
- Detect StateWaking state
- Call waitWaking.Wait()
- Check if StateReady was reached
- If not, return error
```

### Updated start() Method

**Current check** (process.go:239-244):
```go
if currentState == StateSleeping {
    return p.Wake()
}
```

**New check**:
```go
if currentState == StateSleepPending || currentState == StateAsleep {
    return p.Wake()
}
```

Better yet, handle both in the state check:
```go
currentState := p.CurrentState()
switch currentState {
case StateStopped:
    // Normal start flow
case StateSleepPending, StateAsleep:
    // Wake from sleep instead
    return p.Wake()
default:
    return fmt.Errorf("cannot start from state %s", currentState)
}
```

## Testing Plan

### Unit Tests

1. **State Transition Tests**:
   - Test StateReady → StateSleepPending → StateAsleep transition
   - Test StateAsleep → StateWaking → StateReady transition
   - Test StateSleepPending → StateStopping on cmdSleep failure
   - Test StateWaking → StateStopping on cmdWake failure
   - Test invalid transitions are rejected by isValidTransition()

2. **Concurrency Tests**:
   - Test concurrent Sleep() calls
     - First call enters StateSleepPending
     - Second call waits on waitSleeping.Wait()
     - Verify second call completes after first reaches StateAsleep
   - Test concurrent Wake() calls
     - First call enters StateWaking
     - Second call waits on waitWaking.Wait()
     - Verify second call completes after first reaches StateReady
   - Test Sleep() called while already StateSleepPending (should wait)
   - Test Wake() called while already StateWaking (should wait)
   - Test Sleep() called while StateAsleep (idempotent, return immediately)

3. **WaitGroup Synchronization Tests**:
   - Verify waitSleeping.Add(1) called atomically in swapState()
   - Verify waitWaking.Add(1) called atomically in swapState()
   - Verify waitSleeping.Done() called after Sleep() completes
   - Verify waitWaking.Done() called after Wake() completes
   - Test no deadlocks when operations fail

4. **Error Handling Tests**:
   - cmdSleep fails: verify StateSleepPending → StateStopping transition
   - cmdWake fails: verify StateWaking → StateStopping → restart
   - Concurrent operation during failure: verify proper cleanup

### Integration Tests

1. **Sleep Cycle Test**:
   - Start model → StateReady
   - Call Sleep() → StateSleepPending → StateAsleep
   - Verify state progression with timing

2. **Wake Cycle Test**:
   - Model in StateAsleep
   - Call Wake() → StateWaking → StateReady
   - Verify health check runs
   - Verify state progression

3. **Concurrent Access Test**:
   - Thread 1: Call Sleep()
   - Thread 2: Call Sleep() 100ms later
   - Verify thread 2 waits and both succeed
   - Verify final state is StateAsleep

4. **start() from Asleep Test**:
   - Model in StateAsleep
   - Call start()
   - Verify it calls Wake() internally
   - Verify transitions: StateAsleep → StateWaking → StateReady

### Regression Tests

Run existing test suite to ensure changes don't break:
- `make test-dev` - Go tests and static checks
- `make test-all` - Including long-running concurrency tests

## Implementation Checklist

### Phase 1: State Definition
- [ ] Rename `StateSleeping` constant to `StateSleepPending` in ProcessState enum (process.go:26-39)
- [ ] Add `StateAsleep` constant to ProcessState enum
- [ ] Update comment to clarify StateSleepPending (transitional) vs StateAsleep (final)
- [ ] Update isValidTransition() function (process.go:195-213)
  - [ ] Change `case StateSleeping:` to `case StateSleepPending: return to == StateAsleep || to == StateStopping`
  - [ ] Update `case StateReady: return to == StateStopping || to == StateSleepPending`
  - [ ] Add `case StateAsleep: return to == StateWaking || to == StateStopping`

### Phase 2: Synchronization Primitives
- [ ] Add `waitSleeping sync.WaitGroup` to Process struct (process.go:48-89)
- [ ] Add `waitWaking sync.WaitGroup` to Process struct
- [ ] Update swapState() to increment waitSleeping when entering StateSleepPending
- [ ] Update swapState() to increment waitWaking when entering StateWaking

Modify swapState() (process.go:166-192):
```go
// Atomically increment WaitGroups for transitional states
switch newState {
case StateStarting:
    p.waitStarting.Add(1)
case StateSleepPending:
    p.waitSleeping.Add(1)
case StateWaking:
    p.waitWaking.Add(1)
}
```

### Phase 3: Update Sleep() Method
Modify Sleep() method (process.go:467-511):

- [ ] Change state swap to expect StateReady → StateSleepPending
- [ ] Add defer waitSleeping.Done() after state swap
- [ ] Add concurrent call detection:
  ```go
  if curState == StateSleepPending {
      p.waitSleeping.Wait()
      if state := p.CurrentState(); state == StateAsleep {
          return nil // Already asleep
      } else {
          return fmt.Errorf("sleep operation failed, state: %v", state)
      }
  }
  if curState == StateAsleep {
      return nil // Already asleep, idempotent
  }
  ```
- [ ] After successful cmdSleep execution, swap to StateAsleep:
  ```go
  if curState, err := p.swapState(StateSleepPending, StateAsleep); err != nil {
      return fmt.Errorf("failed to transition to asleep: %v", err)
  }
  ```
- [ ] On cmdSleep failure, keep existing StateSleepPending → StateStopping transition

### Phase 4: Update Wake() Method
Modify Wake() method (process.go:551-616):

- [ ] Change initial state check from StateSleeping to StateAsleep:
  ```go
  if currentState != StateAsleep && currentState != StateSleepPending {
      return fmt.Errorf("cannot wake from state %s", currentState)
  }
  ```
- [ ] Update state swap from StateSleeping → StateWaking to StateAsleep → StateWaking:
  ```go
  expectedState := StateAsleep
  if p.CurrentState() == StateSleepPending {
      // Handle edge case where wake called during sleep
      expectedState = StateSleepPending
  }
  if curState, err := p.swapState(expectedState, StateWaking); err != nil {
      // ...
  }
  ```
- [ ] Add defer waitWaking.Done() after state swap
- [ ] Add concurrent call detection:
  ```go
  if curState == StateWaking {
      p.waitWaking.Wait()
      if state := p.CurrentState(); state == StateReady {
          return nil
      } else {
          return fmt.Errorf("wake operation failed, state: %v", state)
      }
  }
  ```
- [ ] Keep existing health check and StateReady transition

### Phase 5: Update start() Method
Modify start() method (process.go:233-387):

- [ ] Update sleep state check (currently line 241):
  ```go
  currentState := p.CurrentState()
  if currentState == StateSleepPending || currentState == StateAsleep {
      p.proxyLogger.Debugf("<%s> Process is asleep, waking instead of starting", p.ID)
      return p.Wake()
  }
  ```
- [ ] Or use switch statement for clarity:
  ```go
  switch currentState {
  case StateStopped:
      // Continue with normal start flow
  case StateSleepPending, StateAsleep:
      return p.Wake()
  case StateStarting:
      // Existing waitStarting logic
  default:
      return fmt.Errorf("cannot start from state %s", currentState)
  }
  ```

### Phase 6: Update Comments and Documentation
- [ ] Remove or update TODO comment at process.go:36
- [ ] Remove or update TODO comment at process.go:184
- [ ] Add code comments explaining two-state pattern for sleep/wake
- [ ] Update any inline documentation

### Phase 7: Event Handling
- [ ] Verify ProcessStateChangeEvent works with new StateAsleep and StateSleepPending
- [ ] Update any event listeners or UI code that checks StateSleeping (rename to StateSleepPending)
- [ ] Search codebase for string "sleeping" to find hardcoded state checks
- [ ] Update all references from StateSleeping to StateSleepPending throughout codebase

### Phase 8: Testing
- [ ] Write unit tests for state transitions
- [ ] Write concurrency tests for Sleep() and Wake()
- [ ] Write WaitGroup synchronization tests
- [ ] Run `make test-dev`
- [ ] Fix any failing tests
- [ ] Run `make test-all`
- [ ] Test with real vLLM instance if available

### Phase 9: Validation
- [ ] Check all state transition paths in code
- [ ] Verify no deadlock scenarios
- [ ] Verify all error paths properly clean up WaitGroups
- [ ] Performance test: ensure WaitGroups don't add significant overhead
- [ ] Review logs for state progression clarity

## Risks and Mitigations

### Risk: Breaking Existing Sleep/Wake Functionality
- **Impact**: High - Sleep mode already implemented and potentially in use
- **Mitigation**:
  - Thorough testing with `make test-dev` and `make test-all`
  - Manual testing with real vLLM instance
  - Keep fallback to Stop() behavior intact

### Risk: WaitGroup Deadlocks
- **Impact**: Critical - Could hang the entire process
- **Mitigation**:
  - Always use defer waitSleeping.Done() / waitWaking.Done()
  - Test all error paths to ensure Done() is called
  - Add timeout-based tests for deadlock detection

### Risk: Race Conditions in State Transitions
- **Impact**: High - Could cause incorrect state transitions
- **Mitigation**:
  - Use existing swapState() atomicity guarantees
  - Add comprehensive concurrency tests
  - Run with -race flag during testing

### Risk: Incomplete Migration (Missing StateAsleep/StateSleepPending Checks)
- **Impact**: Medium - Some code might still check for old StateSleeping only
- **Mitigation**:
  - Grep codebase for "StateSleeping" and "sleeping" string
  - Update all state checks to handle both StateSleepPending and StateAsleep where appropriate
  - Use switch statements instead of if/else for state checks
  - Rename StateSleeping to StateSleepPending consistently throughout codebase

## Future Enhancements

1. **Metrics and Monitoring**:
   - Track time spent in StateSleepPending vs StateAsleep
   - Monitor concurrent Sleep()/Wake() call frequency
   - Alert on excessive wait times for WaitGroups

2. **Configurable Timeouts**:
   - Add cmdSleepTimeout and cmdWakeTimeout config options
   - Currently hardcoded in code (process.go:435-463)

3. **State Persistence**:
   - Remember StateAsleep across llama-swap restarts
   - Reconnect to already-sleeping processes

4. **Predictive Waking**:
   - Start waking process before request arrives
   - Reduce perceived latency

## References

- Process state management: proxy/process.go:26-39
- State transition logic: proxy/process.go:195-213
- swapState() implementation: proxy/process.go:166-192
- waitStarting pattern: proxy/process.go:182-187, 255-261
- Sleep() method: proxy/process.go:467-511
- Wake() method: proxy/process.go:551-616
- start() method: proxy/process.go:233-387
- Existing vLLM sleep plan: ai-plans/vllm-sleep-mode-support.md
