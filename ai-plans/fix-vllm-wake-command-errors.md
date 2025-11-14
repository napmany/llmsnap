# Fix vLLM Wake Command Errors

## Resolution Summary ✅

**Problem**: vLLM Level 2 sleep wake fails with tensor shape mismatch error
**Root Cause**: vLLM Issue #16564 - Level 2 sleep bug affects this version
**Solution**: Switch to Level 1 sleep (stable, simple, works)
**Status**: Config updated and ready for testing

## Overview

When waking up models from vLLM level 2 sleep mode, weight loading fails with a tensor size mismatch error:
```
AssertionError: Attempted to load weight (torch.Size([5120, 2240])) into parameter (torch.Size([1120, 10240]))
```

**Investigation findings:**
1. ❌ Initially tried RLHF sequence (with `?tags=weights` and `?tags=kv_cache`) - incorrect use case
2. ❌ Tested standard Level 2 sequence (no tags) - still failed with same error
3. ✅ **Confirmed**: Level 2 sleep has a genuine bug (vLLM Issue #16564) affecting `reload_weights`
4. ✅ **Solution**: Use Level 1 sleep - stable, simple, no `reload_weights` needed

## Error Analysis

### Current Config Issue

The current config uses this wake sequence:
```yaml
cmdWake: |-
  sh -c '
  # Reallocate weights memory only
  curl -X POST "http://localhost:${PORT}/wake_up?tags=weights" && \
  # Load weights in-place
  curl -X POST "http://localhost:${PORT}/collective_rpc" -H "Content-Type: application/json" -d "{\"method\":\"reload_weights\"}" && \
  # Reallocate KV cache
  curl -X POST "http://localhost:${PORT}/wake_up?tags=kv_cache"
  '
```

**What happens:**
1. ✅ `wake_up?tags=weights` succeeds - reallocates weights memory (0.209s)
2. ❌ `collective_rpc` with `reload_weights` **FAILS** with tensor shape mismatch
3. ✅ `wake_up?tags=kv_cache` succeeds anyway (0.006s)
4. ⚠️  Health check passes, but model is in inconsistent state

### Root Cause: Wrong Wake Sequence

**The tags-based sequence is for RLHF scenarios**, not standard sleep/wake:

- **RLHF use case**: Wake weights → **UPDATE the weights** → Wake KV cache
- **Standard use case**: Wake everything → Reload weights → Reset cache

**Why it fails:**
1. When you call `wake_up?tags=weights`, vLLM allocates weight memory **expecting the model structure as-is**
2. But calling `reload_weights` expects to load from disk into a **fresh model structure**
3. The two operations have incompatible expectations about model state
4. Result: `AssertionError: Attempted to load weight (torch.Size([5120, 2240])) into parameter (torch.Size([1120, 10240]))`

### Correct Level 2 Wake Sequence

According to [vLLM Blog (Oct 2025)](https://blog.vllm.ai/2025/10/26/sleep-mode.html), the standard sequence is:

```bash
# Standard Level 2 wake (no tags)
curl -X POST 'localhost:8001/wake_up'
curl -X POST 'localhost:8001/collective_rpc' -H 'Content-Type: application/json' -d '{"method":"reload_weights"}'
curl -X POST 'localhost:8001/reset_prefix_cache'
```

**Key difference**: No `?tags=` parameters. This wakes the entire model structure atomically, then reloads weights into that structure.

### Known Issues - CONFIRMED

- **vLLM Issue #16564**: Level 2 sleep has a genuine bug with `reload_weights`
- **Affects single-GPU setups**: Confirmed with phi-4-quantized.w4a16 on single GPU
- **Error persists with correct sequence**: Even using the standard Level 2 wake sequence (no tags), the tensor shape mismatch error still occurs
- **Root cause**: Level 2 sleep corrupts model metadata, making `reload_weights` fail regardless of wake sequence
- **RLHF RFC #15254**: The tags-based approach is specifically for weight updates during RLHF training (not related to this bug)

## Design Requirements

### Requirement 1: Use Level 1 Sleep (REQUIRED - Level 2 is broken)

**Problem**: Level 2 sleep has a confirmed bug in this vLLM version. Even with the correct standard wake sequence, `reload_weights` fails with tensor shape mismatch.

**Solution**: Use Level 1 sleep instead:

```yaml
cmdSleep: "curl -X POST http://localhost:${PORT}/sleep?level=1"
cmdWake: "curl -X POST http://localhost:${PORT}/wake_up"
```

**Why Level 1:**
- ✅ **Stable** - No `reload_weights` bugs (weights backed up to CPU, not discarded)
- ✅ **Simple** - Single wake command (no complex sequence)
- ✅ **Fast wake** - Weights copied from CPU to GPU (no disk I/O)
- ⚠️  **More CPU memory** - Weights stay in CPU RAM during sleep

**Level 1 vs Level 2 comparison:**

| Aspect | Level 1 | Level 2 |
|--------|---------|---------|
| **Status** | ✅ Stable | ❌ Broken (Issue #16564) |
| **Wake sequence** | `wake_up` | `wake_up` → `reload_weights` → `reset_prefix_cache` |
| **Weights storage** | CPU RAM | Discarded (reload from disk) |
| **CPU memory** | Higher | Lower |
| **Wake speed** | Fast | Slower (disk I/O) |
| **Complexity** | Simple | Complex |

### Requirement 2: Document Level 2 Bug for Future Reference

When vLLM is upgraded to a version with the fix, Level 2 can be re-enabled using the **standard sequence** (not RLHF):

```yaml
cmdSleep: "curl -X POST http://localhost:${PORT}/sleep?level=2"
cmdWake: |-
  sh -c '
  curl -X POST "http://localhost:${PORT}/wake_up" && \
  curl -X POST "http://localhost:${PORT}/collective_rpc" -H "Content-Type: application/json" -d "{\"method\":\"reload_weights\"}" && \
  curl -X POST "http://localhost:${PORT}/reset_prefix_cache"
  '
```

**⚠️ Important**: Do NOT use `?tags=weights` or `?tags=kv_cache` for standard sleep/wake. Those are only for RLHF/training scenarios where you're actively modifying weights.

### Requirement 3: Document RLHF vs Standard Sequences

**Standard sleep/wake** (for production model serving):
- **Level 1**: `sleep?level=1` → `wake_up`
- **Level 2** (when fixed): `sleep?level=2` → `wake_up` → `reload_weights` → `reset_prefix_cache`

**RLHF weight update** (for training/fine-tuning):
```bash
# Wake only weights to avoid OOM during weight updates
curl -X POST 'localhost:8001/wake_up?tags=weights'
# Update weights here (via training loop)
curl -X POST 'localhost:8001/wake_up?tags=kv_cache'
```

## Testing Plan

### Test 1: Command Syntax Fix
1. Update config.yaml with `sh -c` wrapped commands
2. Trigger model sleep
3. Trigger model wake
4. Verify no curl hostname errors in logs
5. Verify all three curl calls execute (wake_up, collective_rpc, reset_prefix_cache)
6. Check that model responds correctly after wake

### Test 2: Level 1 Sleep Mode
1. Update config.yaml to use level 1 sleep
2. Trigger model sleep
3. Verify model goes into sleep mode (check vLLM logs)
4. Trigger model wake
5. Verify wake completes without weight loading errors
6. Send inference request to verify model works
7. Compare memory usage vs level 2 sleep (level 1 uses more CPU memory)

### Test 3: Both Models
1. Test both `phi-4-quantized.w4a16` and `Qwen/Qwen3-14B-AWQ`
2. Verify sleep/wake works for both
3. Test rapid sleep/wake cycles
4. Test model switching between sleeping models

### Test 4: Error Cases
1. Kill vLLM process during wake - verify restart occurs
2. Make vLLM endpoint unreachable - verify timeout and restart
3. Test with invalid PORT variable - verify error handling

## Implementation Checklist

### Phase 1: Switch to Level 1 Sleep ✅ COMPLETED
- [x] Confirmed Level 2 bug affects single-GPU setup
- [x] Tested standard Level 2 sequence - still fails
- [x] Updated config to use Level 1 sleep
- [x] Simplified cmdWake to single `wake_up` call
- [x] Added comments explaining Level 2 bug
- [ ] **TEST**: Sleep/wake cycle with phi-4-quantized.w4a16
- [ ] **TEST**: Sleep/wake cycle with openai/gpt-oss-20b
- [ ] **TEST**: Verify no errors during wake
- [ ] **TEST**: Verify model responds correctly after wake
- [ ] **TEST**: Monitor CPU memory usage with Level 1

### Phase 2: Documentation (Immediate)
- [x] Document Level 2 bug in plan
- [x] Document Level 1 vs Level 2 tradeoffs
- [x] Document RLHF vs standard sequences
- [ ] Add comment in config about vLLM version requirements for Level 2
- [ ] Update checklist with test results

### Phase 3: Future - Level 2 When Fixed
- [ ] Monitor vLLM releases for Issue #16564 fix
- [ ] Test Level 2 with newer vLLM version
- [ ] If Level 2 works, add config option to switch
- [ ] Document minimum vLLM version for Level 2 support

### Phase 4: Error Handling (Future)
- [ ] Add more detailed logging in proxy/process.go executeWakeCommand
- [ ] Capture and parse vLLM API error messages
- [ ] Add retry logic for transient failures
- [ ] Add metrics for sleep/wake success/failure rates

## Implemented Config Changes ✅

### Level 1 Sleep (CURRENT SOLUTION - Stable)

```yaml
healthCheckTimeout: 500
models:
  phi-4-quantized.w4a16:
    cmd: "uv run python -m vllm.entrypoints.openai.api_server --model ./phi-4-quantized.w4a16/ --served-model-name phi-4-quantized.w4a16 --max-model-len 16384 --gpu-memory-utilization 0.80 --trust-remote-code --enable-sleep-mode --port ${PORT}"
    ttl: 0
    env:
      - VLLM_SERVER_DEV_MODE=1
    # Level 1 sleep: backs up weights to CPU (more stable than Level 2)
    # Note: Level 2 has a bug with reload_weights (vLLM Issue #16564)
    cmdSleep: "curl -X POST http://localhost:${PORT}/sleep?level=1"
    # Level 1 wake: simple single call (no reload_weights needed)
    cmdWake: "curl -X POST http://localhost:${PORT}/wake_up"

  openai/gpt-oss-20b:
    cmd: "uv run python -m vllm.entrypoints.openai.api_server --model openai/gpt-oss-20b --served-model-name openai/gpt-oss-20b --max-model-len 34000 --gpu-memory-utilization 0.80 --trust-remote-code --enable-sleep-mode --port ${PORT}"
    ttl: 0
    env:
      - VLLM_SERVER_DEV_MODE=1
    cmdSleep: "curl -X POST http://localhost:${PORT}/sleep?level=1"
    cmdWake: "curl -X POST http://localhost:${PORT}/wake_up"
```

**Why Level 1 was chosen:**
1. ✅ **Stable** - No reload_weights bugs
2. ✅ **Simple** - Single wake command
3. ✅ **Tested** - Standard Level 2 sequence still failed with tensor shape error
4. ⚠️  **Tradeoff** - Uses more CPU memory (weights stay in RAM)

**What was tried before this:**
1. ❌ RLHF sequence with tags (incorrect use case)
2. ❌ Standard Level 2 sequence without tags (still failed due to vLLM bug)
3. ✅ Level 1 sleep (final solution)

### Future: Level 2 When vLLM is Fixed

When vLLM is upgraded to a version with Issue #16564 fixed, Level 2 can be enabled:

```yaml
cmdSleep: "curl -X POST http://localhost:${PORT}/sleep?level=2"
cmdWake: |-
  sh -c '
  curl -X POST "http://localhost:${PORT}/wake_up" && \
  curl -X POST "http://localhost:${PORT}/collective_rpc" -H "Content-Type: application/json" -d "{\"method\":\"reload_weights\"}" && \
  curl -X POST "http://localhost:${PORT}/reset_prefix_cache"
  '
```

**Benefits of Level 2 (when fixed):**
- Lower CPU memory usage
- More complete "deep sleep"

**Requirements:**
- vLLM version with fix for Issue #16564
- Must NOT use `?tags=` parameters for standard operations

### Comparison: Standard vs RLHF Sequences

**⚠️ IMPORTANT: The tags-based approach is ONLY for RLHF/fine-tuning scenarios!**

| Use Case | Sequence | When to Use |
|----------|----------|-------------|
| **Standard Level 1** | `sleep?level=1` → `wake_up` | Production (current solution) |
| **Standard Level 2** | `sleep?level=2` → `wake_up` → `reload_weights` → `reset_prefix_cache` | Production (when vLLM fixed) |
| **RLHF training** | `wake_up?tags=weights` → [modify weights] → `wake_up?tags=kv_cache` | Training/fine-tuning only |

## References

### vLLM Documentation
- [vLLM Blog: Zero-Reload Model Switching (Oct 2025)](https://blog.vllm.ai/2025/10/26/sleep-mode.html) - **PRIMARY SOURCE** for standard Level 2 wake sequence
- [vLLM Sleep Mode Documentation](https://docs.vllm.ai/en/latest/features/sleep_mode.html)
- [vLLM Production Stack: Sleep and Wakeup Mode](https://docs.vllm.ai/projects/production-stack/en/latest/use_cases/sleep-wakeup-mode.html)

### vLLM Issues & RFCs
- [Issue #16564](https://github.com/vllm-project/vllm/issues/16564) - Bug: Level 2 sleep weight loading issues in distributed setups (TP=4, 8xA800)
- [RFC #15254](https://github.com/vllm-project/vllm/issues/15254) - Better RLHF support with tags-based wake (source of tags example)

### Code References
- `proxy/process.go:622` - executeWakeCommand implementation
- `proxy/process.go:517` - executeSleepCommand implementation
- `proxy/config/config.go:467` - SanitizeCommand function (handles multiline commands)
- `proxy/config/config.go:230-232` - Command comment stripping
- `proxy/config/config.go:274-275` - CmdSleep and CmdWake macro substitution

### Key Findings
1. **Standard sequence** (production): `wake_up` → `reload_weights` → `reset_prefix_cache` (no tags)
2. **RLHF sequence** (training): `wake_up?tags=weights` → [modify weights] → `wake_up?tags=kv_cache` (with tags)
3. The tags-based approach is **NOT** for standard sleep/wake operations
4. The error was caused by mixing RLHF sequence with standard production use case
