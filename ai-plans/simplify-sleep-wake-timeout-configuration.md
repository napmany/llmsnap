# Simplify Sleep/Wake Timeout Configuration

**Status:** ✅ COMPLETED

## Overview

Refactor sleep/wake timeout configuration to push timeout values directly to HTTPEndpoint structures during config loading, eliminating intermediate ModelConfig fields and runtime resolution methods. This simplifies the timeout hierarchy and makes configuration more transparent.

## Current State Analysis

### Current Timeout Resolution Architecture

1. **Global Config Level** (`config.go`)
   - `Config.SleepRequestTimeout` (default: 10s)
   - `Config.WakeRequestTimeout` (default: 10s)

2. **Per-Endpoint Level** (`model_config.go`)
   - `HTTPEndpoint.Timeout` (optional, per-endpoint override)

3. **Runtime Resolution** (`process.go`)
   - `Process.globalSleepTimeout` and `Process.globalWakeTimeout` (stored from global config)
   - `getSleepTimeout()` method: returns `globalSleepTimeout` converted to time.Duration
   - `getWakeTimeout()` method: returns `globalWakeTimeout` converted to time.Duration
   - `sendHTTPRequest()`: uses endpoint timeout if set, else uses passed defaultTimeout from getter

### Problems with Current Design

1. **Split configuration**: Global timeouts are stored separately in Process instead of being part of ModelConfig
2. **Redundant methods**: `getSleepTimeout()` and `getWakeTimeout()` only pass through the global values
3. **Extra indirection**: Process stores `globalSleepTimeout` and `globalWakeTimeout` separately instead of using model config directly
4. **Unnecessary complexity**: Three parameters passed to NewProcess just to store in two fields

## Design Requirements

### 2. Copy Global Timeouts to ModelConfig in LoadConfigFromReader

In `config.go`, after loading the YAML config, copy global timeout values to each model's config.

**Implementation location:** In `LoadConfigFromReader()` after line 418 (after model processing loop), add:

```go
// Copy global sleep/wake timeouts to each model config
for modelId, modelConfig := range config.Models {
    modelConfig.SleepRequestTimeout = config.SleepRequestTimeout
    modelConfig.WakeRequestTimeout = config.WakeRequestTimeout
    config.Models[modelId] = modelConfig
}
```

**Rationale:** This makes the timeout values available directly in ModelConfig, which Process already has access to. No need to pass them separately or store them in Process.

### 3. Update Process Struct

In `process.go`:

**Remove fields:**
- Line 68: Remove `globalSleepTimeout int`
- Line 69: Remove `globalWakeTimeout int`

**Update NewProcess function:**
- Lines 133-134: Remove initialization of `globalSleepTimeout` and `globalWakeTimeout`
- Remove `sleepTimeout int, wakeTimeout int` parameters from function signature (line 100)

### 4. Remove Timeout Resolution Methods

In `process.go`, remove these methods entirely:
- Lines 461-472: Remove `getSleepTimeout()` method
- Lines 474-485: Remove `getWakeTimeout()` method

### 5. Update Timeout Usage

In `process.go`:

**Update `sendSleepRequests()` (line 560):**
```go
func (p *Process) sendSleepRequests() error {
    if len(p.config.SleepEndpoints) == 0 {
        return fmt.Errorf("no sleep endpoints configured")
    }

    // Use timeout directly from config (no getter method needed)
    defaultTimeout := time.Duration(p.config.SleepRequestTimeout) * time.Second
    p.proxyLogger.Infof("<%s> Executing %d sleep request(s) (default timeout: %v)",
        p.ID, len(p.config.SleepEndpoints), defaultTimeout)

    for i, endpoint := range p.config.SleepEndpoints {
        // ... rest unchanged ...
    }
}
```

**Update `sendWakeRequests()` (line 584):**
```go
func (p *Process) sendWakeRequests() error {
    if len(p.config.WakeEndpoints) == 0 {
        return fmt.Errorf("no wake endpoints configured")
    }

    // Use timeout directly from config (no getter method needed)
    defaultTimeout := time.Duration(p.config.WakeRequestTimeout) * time.Second
    p.proxyLogger.Infof("<%s> Executing %d wake request(s) (default timeout: %v)",
        p.ID, len(p.config.WakeEndpoints), defaultTimeout)

    for i, endpoint := range p.config.WakeEndpoints {
        // ... rest unchanged ...
    }
}
```

### 6. Update Process Instantiation Call Sites

Find all calls to `NewProcess()` and remove the `sleepTimeout` and `wakeTimeout` parameters.

**Call sites to update:**

1. **Production code:**
   - `proxy/processgroup.go:49` - Main call site (after fixing field names above)

2. **Test code** (`proxy/process_test.go`):
   - Line 36
   - Line 72
   - Line 100
   - Line 125
   - Line 167
   - Line 194
   - Line 267
   - Line 300
   - Line 335
   - Line 353
   - Line 378
   - Line 414
   - Line 464
   - Line 484
   - Line 485
   - Line 520

All test calls currently pass `60, 60` for sleep/wake timeouts. After this refactoring, remove these two parameters from all calls.

## Benefits

1. **Simpler architecture**: Timeout values are part of ModelConfig, not stored separately in Process
2. **Less code**: Removes two getter methods, two Process fields, and two NewProcess parameters
3. **Easier testing**: Timeouts can be set directly in ModelConfig for testing
4. **Better encapsulation**: Process uses its config directly without storing redundant data
5. **Clearer data flow**: Global config values copied to ModelConfig during loading, then used directly

## Testing Plan

### Unit Tests

1. **Test timeout copying in config loading** (`config_test.go`):
   ```go
   func TestTimeoutCopyToModelConfig(t *testing.T) {
       // Test that global timeouts are copied to each model's config
       // Verify ModelConfig.SleepRequestTimeout == Config.SleepRequestTimeout
       // Verify ModelConfig.WakeRequestTimeout == Config.WakeRequestTimeout
   }
   ```

2. **Test existing sleep/wake tests still pass** (`process_test.go`):
   - Verify existing sleep/wake tests work after removing timeout parameters from NewProcess
   - Tests may need ModelConfig objects with timeout fields set

### Integration Tests

1. Run `make test-dev` to verify no regressions
2. Run `make test-all` to verify concurrency tests pass
3. Manual testing with config files that:
   - Set global sleep/wake timeouts (verify they're used)
   - Use default timeouts (verify 10s defaults work)
   - Set per-endpoint timeouts (verify they still override)

## Pre-requisites: Fix Current Compilation Error

**IMPORTANT**: The current code does not compile. There's a field name mismatch:

```
proxy/processgroup.go:49:74: pg.config.CmdSleepTimeout undefined (type config.Config has no field or method CmdSleepTimeout)
proxy/processgroup.go:49:101: pg.config.CmdWakeTimeout undefined (type config.Config has no field or method CmdWakeTimeout)
```

Before implementing this plan, fix the compilation error:

In `proxy/processgroup.go` line 49, change:
```go
process := NewProcess(modelID, pg.config.HealthCheckTimeout, pg.config.CmdSleepTimeout, pg.config.CmdWakeTimeout, modelConfig, pg.upstreamLogger, pg.proxyLogger)
```

To:
```go
process := NewProcess(modelID, pg.config.HealthCheckTimeout, pg.config.SleepRequestTimeout, pg.config.WakeRequestTimeout, modelConfig, pg.upstreamLogger, pg.proxyLogger)
```

Verify the fix builds: `go build ./...`

## Checklist

### Pre-work

- [ ] Fix compilation error in `proxy/processgroup.go` line 49
  - [ ] Change `pg.config.CmdSleepTimeout` to `pg.config.SleepRequestTimeout`
  - [ ] Change `pg.config.CmdWakeTimeout` to `pg.config.WakeRequestTimeout`
- [ ] Verify code builds: `go build ./...`

### Code Changes

- [ ] Add `SleepRequestTimeout` and `WakeRequestTimeout` fields to `ModelConfig` in `proxy/config/model_config.go`
- [ ] Add timeout copying logic in `LoadConfigFromReader()` in `proxy/config/config.go`
  - [ ] Add after line 418 (after model processing loop)
  - [ ] Copy `config.SleepRequestTimeout` to each `modelConfig.SleepRequestTimeout`
  - [ ] Copy `config.WakeRequestTimeout` to each `modelConfig.WakeRequestTimeout`
- [ ] Update `Process` struct in `proxy/process.go`
  - [ ] Remove `globalSleepTimeout` field (line 68)
  - [ ] Remove `globalWakeTimeout` field (line 69)
- [ ] Update `NewProcess()` function in `proxy/process.go`
  - [ ] Remove `sleepTimeout int` parameter (line 100)
  - [ ] Remove `wakeTimeout int` parameter (line 100)
  - [ ] Remove initialization of `globalSleepTimeout` (line 133)
  - [ ] Remove initialization of `globalWakeTimeout` (line 134)
- [ ] Remove timeout getter methods in `proxy/process.go`
  - [ ] Remove `getSleepTimeout()` method (lines 461-472)
  - [ ] Remove `getWakeTimeout()` method (lines 474-485)
- [ ] Update `sendSleepRequests()` in `proxy/process.go`
  - [ ] Replace `p.getSleepTimeout()` with `time.Duration(p.config.SleepRequestTimeout) * time.Second` (line 565)
- [ ] Update `sendWakeRequests()` in `proxy/process.go`
  - [ ] Replace `p.getWakeTimeout()` with `time.Duration(p.config.WakeRequestTimeout) * time.Second` (line 589)
- [ ] Update all `NewProcess()` call sites to remove sleep/wake timeout parameters
  - [ ] Production: `proxy/processgroup.go:49`
  - [ ] Tests: Update all 17 calls in `proxy/process_test.go` (lines 36, 72, 100, 125, 167, 194, 267, 300, 335, 353, 378, 414, 464, 484, 485, 520)

### Testing

- [ ] Add unit test for timeout copying in config loading
- [ ] Run `make test-dev` and fix any failures
- [ ] Run `make test-all` and verify all tests pass
- [ ] Manual test with config setting global timeouts (verify they're used)
- [ ] Manual test with config using default timeouts (verify 10s defaults work)
- [ ] Manual test with per-endpoint timeouts (verify they still override)

### Documentation

- [ ] Update any comments referencing the old getSleepTimeout/getWakeTimeout methods
- [ ] Add comment in `ModelConfig` documenting that timeout fields are copied from global config
- [ ] Update config.go comments to explain timeout copying logic

## Notes

- **No YAML changes**: The new ModelConfig fields are NOT exposed in YAML. They are populated programmatically during config loading.
- **Backward compatibility**: Existing config files work unchanged. The defaults (10s) remain the same.
- **Per-endpoint timeouts**: HTTPEndpoint.Timeout continues to work as before - it overrides the model's default timeout when set.
- **Simpler architecture**: Removes redundant storage and getter methods. Process uses config directly.
- **Future enhancement**: Could add optional per-model `sleepRequestTimeout` / `wakeRequestTimeout` YAML fields to ModelConfig for per-model overrides of global defaults.
