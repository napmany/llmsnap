# UI Support for Sleep/Wake States

## Title
Update UI to support sleep/wake process states and controls

## Overview

The backend has been enhanced with sleep/wake functionality for models, introducing three new process states (`sleepPending`, `asleep`, `waking`) and corresponding methods (`Sleep()`, `wake()`). The UI needs to be updated to:

1. Display the new sleep/wake states with appropriate visual styling
2. Provide user controls to manually trigger sleep operations
3. Handle state transitions gracefully in the real-time event stream
4. Maintain consistency between backend state names and UI display

This plan outlines the changes needed to integrate the new sleep/wake states into the React-based UI, ensuring users have full visibility and control over model lifecycle management.

### Current State Analysis

**Backend States** (from proxy/process.go:27-41):
- `stopped` - Process is not running
- `starting` - Process is initializing
- `ready` - Process is running and ready to handle requests
- `stopping` - Process is shutting down
- `shutdown` - Process is permanently shut down
- `sleepPending` - Process is transitioning to sleep (NEW)
- `asleep` - Process is sleeping (NEW)
- `waking` - Process is waking from sleep (NEW)

**UI State Type** (from ui/src/contexts/APIProvider.tsx:4):
```typescript
type ModelStatus = "ready" | "starting" | "stopping" | "stopped" | "shutdown" | "unknown";
```

**Gap**: The UI does not yet support the new sleep/wake states.

## Design Requirements

### 1. TypeScript Type Updates

**File**: `ui/src/lib/types.ts` or `ui/src/contexts/APIProvider.tsx`

Update the `ModelStatus` type to include the new sleep/wake states:

```typescript
type ModelStatus =
  | "ready"
  | "starting"
  | "stopping"
  | "stopped"
  | "shutdown"
  | "sleepPending"  // NEW
  | "asleep"        // NEW
  | "waking"        // NEW
  | "unknown";
```

**Rationale**: TypeScript needs to be aware of all possible state values to provide type safety and prevent runtime errors.

### 2. CSS Styling for New States

**File**: `ui/src/index.css`

Add visual styles for the new sleep/wake states to the status badge system (around line 136):

```css
/* Existing styles */
.status--ready {
  @apply bg-success/10 text-success;
}

.status--starting,
.status--stopping {
  @apply bg-warning/10 text-warning;
}

.status--stopped {
  @apply bg-error/10 text-error;
}

/* NEW: Sleep/wake state styles */
.status--sleepPending,
.status--waking {
  @apply bg-info/10 text-info;  /* Transitional states: similar to starting/stopping */
}

.status--asleep {
  @apply bg-primary/10 text-primary;  /* Distinct sleep state: use primary color */
}

.status--shutdown {
  @apply bg-error/20 text-error;  /* Permanent shutdown: darker error color */
}
```

**Design Choices**:
- `sleepPending` and `waking`: Use info colors (similar to warning) to indicate transitional states
- `asleep`: Use primary color to distinguish from stopped (different lifecycle phase)
- Visual hierarchy: ready (success/green) → asleep (primary/blue) → stopped (error/red)

### 3. Display State Names in UI

**File**: `ui/src/pages/Models.tsx`

The state is currently displayed as-is from the backend (line 183):
```typescript
<span className={`w-16 text-center status status--${model.state}`}>{model.state}</span>
```

This will automatically work with the new states once CSS is added. However, consider adding human-friendly display names:

**Option A**: Display state names as-is (simplest, current behavior)
- `sleepPending` → "sleepPending"
- `asleep` → "asleep"
- `waking` → "waking"

**Option B**: Transform state names for better readability
```typescript
const getDisplayStateName = (state: ModelStatus): string => {
  const stateDisplayNames: Record<ModelStatus, string> = {
    ready: "ready",
    starting: "starting",
    stopping: "stopping",
    stopped: "stopped",
    shutdown: "shutdown",
    sleepPending: "sleeping...",  // More user-friendly
    asleep: "asleep",
    waking: "waking...",          // Indicates progress
    unknown: "unknown",
  };
  return stateDisplayNames[state] || state;
};
```

**Recommendation**: Start with Option A for consistency, add Option B if user feedback indicates confusion.

### 4. Sleep/Wake Controls

**File**: `ui/src/pages/Models.tsx`

Currently, the UI shows "Load" for stopped models and "Unload" for ready models (lines 168-180). With sleep/wake support, we need more granular controls:

**Current Logic**:
```typescript
{model.state === "stopped" ? (
  <button className="btn btn--sm" onClick={() => loadModel(model.id)}>
    Load
  </button>
) : (
  <button className="btn btn--sm" onClick={() => unloadSingleModel(model.id)}
    disabled={model.state !== "ready"}>
    Unload
  </button>
)}
```

**New Logic** (consider model sleep configuration):

```typescript
{model.state === "stopped" ? (
  <button className="btn btn--sm" onClick={() => loadModel(model.id)}>
    Load
  </button>
) : model.state === "asleep" ? (
  <button className="btn btn--sm" onClick={() => loadModel(model.id)}>
    Wake
  </button>
) : model.state === "ready" ? (
  <>
    <button className="btn btn--sm" onClick={() => sleepModel(model.id)}>
      Sleep
    </button>
    <button className="btn btn--sm" onClick={() => unloadSingleModel(model.id)}>
      Unload
    </button>
  </>
) : (
  <button className="btn btn--sm" disabled>
    {model.state}
  </button>
)}
```

**Considerations**:
- **Sleep button visibility**: Should only show "Sleep" if the model has sleep endpoints configured. This requires backend to expose a `sleepEnabled` field in the model API response.
- **Wake behavior**: Clicking "Wake" on an asleep model should trigger a load request (the backend will handle wake vs start logic).
- **Transitional states**: During `sleepPending`, `waking`, `starting`, `stopping` - disable all buttons or show current state.
- **Layout**: Two buttons for ready state may require layout adjustments (stack vertically on narrow screens, or use a dropdown menu).

### 5. API Endpoint for Sleep

**File**: `ui/src/contexts/APIProvider.tsx`

Add a new API method to manually trigger sleep:

```typescript
const sleepModel = useCallback(async (model: string) => {
  try {
    const response = await fetch(`/api/models/sleep/${model}`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to sleep model: ${response.status}`);
    }
  } catch (error) {
    console.error("Failed to sleep model:", error);
    throw error;
  }
}, []);
```

Add to the context provider value:
```typescript
const value = useMemo(
  () => ({
    // ... existing values
    sleepModel,  // NEW
  }),
  [models, listModels, unloadAllModels, loadModel, sleepModel, enableAPIEvents, proxyLogs, upstreamLogs, metrics]
);
```

Export in the interface:
```typescript
interface APIProviderType {
  // ... existing fields
  sleepModel: (model: string) => Promise<void>;  // NEW
}
```

**Note**: This assumes the backend will implement `POST /api/models/sleep/:model_id` endpoint (see Phase 5 of vllm-sleep-mode-support.md checklist).

### 6. Model Configuration Display

**File**: `ui/src/contexts/APIProvider.tsx` and `ui/src/pages/Models.tsx`

To show sleep controls conditionally, the Model interface needs a new field:

```typescript
export interface Model {
  id: string;
  state: ModelStatus;
  name: string;
  description: string;
  unlisted: boolean;
  sleepEnabled?: boolean;  // NEW: indicates if cmdSleep/cmdWake are configured
}
```

This requires the backend `/api/models/` endpoint to include `sleepEnabled` in the response. The UI can then conditionally render sleep controls:

```typescript
{model.state === "ready" && model.sleepEnabled && (
  <button className="btn btn--sm" onClick={() => sleepModel(model.id)}>
    Sleep
  </button>
)}
```

### 7. Handle Edge Cases

**Transitional State Handling**:
- During `sleepPending`, `waking`, `starting`, `stopping`: Disable all action buttons
- Show current state name in the button to provide feedback
- Optionally add a loading spinner icon

**Error Handling**:
- If sleep API call fails, show error message (consider toast notification or inline error)
- State will revert or change based on backend error handling

**Real-time Updates**:
- The existing SSE (Server-Sent Events) implementation in APIProvider should automatically update model states
- No changes needed to event handling logic, only ensure new states are properly typed

### 8. Accessibility Considerations

- Ensure status badges have sufficient color contrast (test with dark mode)
- Add `aria-label` attributes to buttons for screen readers:
  ```typescript
  <button aria-label={`Sleep model ${model.id}`}>Sleep</button>
  ```
- Consider adding tooltips to explain sleep vs unload vs stop

### 9. Mobile/Narrow Screen Layout

**File**: `ui/src/pages/Models.tsx`

The current implementation has a dropdown menu for narrow screens (lines 73-111). If adding multiple buttons (Sleep + Unload), consider:

**Option A**: Dropdown menu for all actions
```typescript
{isNarrow && model.state === "ready" && (
  <DropdownMenu>
    <DropdownItem onClick={() => sleepModel(model.id)}>Sleep</DropdownItem>
    <DropdownItem onClick={() => unloadSingleModel(model.id)}>Unload</DropdownItem>
  </DropdownMenu>
)}
```

**Option B**: Single primary button, secondary action in menu
- Primary: "Sleep" (less destructive)
- Secondary: "Unload" (more destructive, in menu)

**Recommendation**: Start with Option A for consistency with existing narrow screen patterns.

## Testing Plan

### Unit Tests

1. **Type Safety Tests**:
   - Ensure TypeScript accepts all new state values
   - Verify no type errors in state transitions
   - Test that unknown states fall back gracefully

2. **Component Tests** (if using React Testing Library):
   - Test that sleep/wake states render with correct CSS classes
   - Test that buttons appear/disappear based on state
   - Test that sleep/wake API calls are made on button click
   - Test disabled states during transitions

### Visual Regression Tests

1. **Status Badge Colors**:
   - Screenshot each state badge in light mode
   - Screenshot each state badge in dark mode
   - Verify colors match design system
   - Check contrast ratios for accessibility (WCAG AA minimum)

2. **Button Layout**:
   - Test with multiple models in different states
   - Test narrow vs wide screen layouts
   - Verify no layout overflow or button clipping

### Integration Tests

1. **State Transitions**:
   - Load model → verify "ready" state
   - Sleep model → verify "sleepPending" → "asleep" transition
   - Wake model → verify "waking" → "ready" transition
   - Unload model → verify "stopping" → "stopped" transition

2. **Real-time Updates**:
   - Trigger state change from backend
   - Verify UI updates via SSE without page refresh
   - Test with multiple models changing states simultaneously

3. **API Error Handling**:
   - Mock failed sleep API call
   - Verify error is displayed to user
   - Verify state remains unchanged or reverts appropriately

### Manual Testing

1. **User Workflow**:
   - Navigate to Models page
   - Load a model with sleep enabled
   - Verify "Sleep" button appears when ready
   - Click "Sleep" and verify state changes
   - Verify model shows "asleep" state
   - Click "Wake" and verify model returns to ready
   - Test "Unload" button functionality
   - Verify "Unload All" handles sleeping models correctly

2. **Cross-browser Testing**:
   - Test on Chrome, Firefox, Safari
   - Test light and dark modes
   - Test responsive layouts (mobile, tablet, desktop)

3. **Edge Cases**:
   - Model without sleep configured (should not show sleep button)
   - Model in transitional states (buttons should be disabled)
   - Multiple models in various states
   - Network disconnection during state change

## Implementation Checklist

### Phase 1: Type Definitions
- [x] Update `ModelStatus` type in `ui/src/contexts/APIProvider.tsx` to include `sleepPending`, `asleep`, `waking`
- [x] Add `sleepEnabled` field to `Model` interface
- [x] Verify TypeScript compilation passes with no errors

### Phase 2: Visual Styling
- [x] Add CSS classes for `status--sleepPending` in `ui/src/index.css`
- [x] Add CSS classes for `status--asleep` in `ui/src/index.css`
- [x] Add CSS classes for `status--waking` in `ui/src/index.css`
- [x] Add CSS classes for `status--shutdown` if missing
- [ ] Test color contrast in light mode (WCAG AA)
- [ ] Test color contrast in dark mode (WCAG AA)

### Phase 3: API Integration
- [x] Add `sleepModel()` function to APIProvider
- [x] Export `sleepModel` in APIProviderType interface
- [x] Export `sleepModel` from useAPI hook
- [x] Add error handling for sleep API calls
- [x] Verify API events still update model states correctly

### Phase 4: UI Controls
- [x] Update button logic in Models.tsx to handle new states
- [x] Add "Sleep" button for ready models (conditionally if sleepEnabled)
- [x] Change "Load" to "Wake" for asleep models
- [x] Add disabled state for transitional states
- [ ] Test button layout on wide screens
- [ ] Test button layout on narrow screens (mobile menu)
- [ ] Add aria-labels for accessibility

### Phase 5: Display Improvements
- [ ] Decide on state name display (as-is vs friendly names)
- [ ] Implement state name transformation if chosen
- [ ] Add tooltips/help text for sleep/wake functionality (optional)
- [ ] Ensure status badges wrap properly on narrow screens

### Phase 6: Backend Dependencies
- [x] Verify backend implements `POST /api/models/sleep/:model_id` endpoint
- [x] Verify backend includes `sleepEnabled` in `/api/models/` response
- [x] Coordinate with backend team on API contract

### Phase 7: Testing
- [x] Run npm build and verify no TypeScript errors
- [ ] Test state display for all states (manual testing)
- [ ] Test sleep button functionality with real backend
- [ ] Test wake button functionality with real backend
- [ ] Test state transitions via SSE updates
- [ ] Test error scenarios (network failures, API errors)
- [ ] Test on multiple browsers
- [ ] Test responsive layouts
- [ ] Verify dark mode styling

### Phase 8: Documentation
- [ ] Update UI README with sleep/wake feature description (if applicable)
- [ ] Add comments to complex state transition logic
- [ ] Document sleep/wake API endpoints used by UI
- [ ] Create user-facing documentation on sleep vs unload

## Design Decisions & Rationale

### State Color Choices

| State | Color | Rationale |
|-------|-------|-----------|
| `ready` | Success (green) | Model is operational and accepting requests |
| `asleep` | Primary (blue) | Model is in a low-power state but can be quickly resumed |
| `stopped` | Error (red) | Model is fully stopped and requires full restart |
| `starting`, `stopping` | Warning (orange) | Transitional states, temporary |
| `sleepPending`, `waking` | Info (gray/blue) | Sleep-related transitions, similar to starting/stopping |
| `shutdown` | Error dark (dark red) | Permanent state, cannot be restarted |

### Button Action Priority

For models in "ready" state with sleep enabled:
1. **Primary action**: Sleep (less destructive, faster to reverse)
2. **Secondary action**: Unload (more destructive, full shutdown)

Rationale: Sleep is the preferred action for temporary model swapping, as it's faster to resume. Unload is for permanent shutdown or when resources need to be fully freed.

### Conditional Sleep Controls

Sleep/wake buttons should only appear when `sleepEnabled` is true. This prevents:
- Confusion for models without sleep configuration
- API errors from attempting to sleep models without sleep endpoints
- UI clutter for users not using sleep mode

### Backward Compatibility

The UI changes are backward compatible:
- Models without sleep configuration show existing Load/Unload buttons
- Older backend versions without sleep states will not send those states via SSE
- Unknown states fall back to "unknown" display
- No breaking changes to existing API contracts (only additions)

## Known Limitations & Future Enhancements

### Current Limitations
1. No visual indicator showing sleep level (level 1 vs level 2 for vLLM)
2. No metrics/timing for sleep/wake operations in Activity view
3. No bulk sleep operation (sleep all ready models)
4. No automatic sleep visualization in UI (only shows state after sleep completes)
5. Cannot configure sleep settings from UI (must edit config file)

### Future Enhancements
1. **Sleep Metrics Dashboard**:
   - Show average wake time per model
   - Display sleep/wake failure rates
   - Graph of sleep/wake operations over time

2. **Advanced Controls**:
   - Bulk sleep/wake operations
   - Configure sleep timeout from UI
   - Select sleep level (for vLLM) from UI

3. **Visual Feedback**:
   - Progress bar during sleep/wake transitions
   - Estimated wake time display
   - Animation for state transitions

4. **Configuration UI**:
   - Edit `cmdSleep` and `cmdWake` commands from UI
   - Test sleep/wake commands before saving
   - Validate sleep endpoint configuration

5. **Status Monitoring**:
   - Real-time resource usage during sleep vs stopped states
   - Memory footprint comparison
   - Quick sleep/wake performance comparison vs full restart

6. **Notifications**:
   - Toast notifications for sleep/wake completion
   - Error notifications for failed operations
   - Badge count for models in sleep state

## References

- Backend sleep/wake implementation: `proxy/process.go` (lines 27-609)
- Backend sleep mode plan: `ai-plans/vllm-sleep-mode-support.md`
- UI TypeScript types: `ui/src/contexts/APIProvider.tsx`
- UI Models page: `ui/src/pages/Models.tsx`
- UI styling: `ui/src/index.css`
- React documentation: https://react.dev/
- Tailwind CSS: https://tailwindcss.com/
