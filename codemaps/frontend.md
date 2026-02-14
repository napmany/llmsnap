# Frontend Codemap

> Freshness: 2026-02-14

## Stack

- **Framework**: Svelte 5.19.0 (runes mode)
- **Build**: Vite 6.3.5, output to `../proxy/ui_dist`
- **Styling**: Tailwind CSS 4.1.8 with custom theme tokens
- **Router**: svelte-spa-router 4.0.1 (hash-based)
- **Icons**: lucide-svelte
- **Markdown**: remark-parse + remark-gfm + remark-math + rehype-katex + highlight.js

## Component Tree

```
App.svelte
├── Header.svelte
│   └── ConnectionStatus.svelte
└── Router
    ├── / → Playground.svelte
    │   ├── ChatInterface.svelte
    │   │   ├── ModelSelector.svelte
    │   │   ├── ExpandableTextarea.svelte
    │   │   └── ChatMessage.svelte
    │   ├── ImageInterface.svelte
    │   │   └── ModelSelector.svelte
    │   ├── SpeechInterface.svelte
    │   │   ├── ModelSelector.svelte
    │   │   └── ExpandableTextarea.svelte
    │   └── AudioInterface.svelte
    │       └── ModelSelector.svelte
    ├── /models → Models.svelte
    │   ├── ResizablePanels.svelte
    │   │   ├── ModelsPanel.svelte
    │   │   └── LogPanel.svelte
    │   └── StatsPanel.svelte
    │       └── TokenHistogram.svelte
    ├── /activity → Activity.svelte
    │   └── CaptureDialog.svelte
    └── /logs → LogViewer.svelte
        └── ResizablePanels.svelte
            ├── LogPanel.svelte (proxy)
            └── LogPanel.svelte (upstream)
```

## Stores

### `stores/api.ts` - Server Communication
- **Writable stores**: `models`, `proxyLogs`, `upstreamLogs`, `metrics`, `versionInfo`
- **SSE connection**: `enableAPIEvents()` with auto-reconnect (exponential backoff)
- **API functions**: `listModels()`, `unloadAllModels()`, `unloadSingleModel()`, `sleepModel()`, `loadModel()`, `getCapture()`
- Log buffer capped at 100KB

### `stores/theme.ts` - UI State
- **Persistent**: `isDarkMode`, `appTitle` (localStorage)
- **Runtime**: `screenWidth`, `connectionState`
- **Derived**: `isNarrow` (responsive breakpoint)

### `stores/persistent.ts` - localStorage Wrapper
- Generic `persistentStore<T>()` with cross-tab sync via `storage` event

## API Integration (`lib/`)

| File | Purpose |
|---|---|
| `chatApi.ts` | `POST /v1/chat/completions` with SSE streaming |
| `imageApi.ts` | `POST /v1/images/generations` |
| `speechApi.ts` | `POST /v1/audio/speech` (returns audio Blob) |
| `audioApi.ts` | `POST /v1/audio/transcriptions` (FormData upload) |
| `types.ts` | Shared TypeScript interfaces |
| `markdown.ts` | Markdown → HTML rendering pipeline |
| `modelUtils.ts` | Model filtering/sorting utilities |

## Key Types (`lib/types.ts`)

```typescript
type ConnectionState = "connected" | "connecting" | "disconnected"
type ModelStatus = "ready" | "starting" | "stopping" | "stopped"
                 | "shutdown" | "sleepPending" | "asleep" | "waking" | "unknown"

interface Model { id, state: ModelStatus, name, description, unlisted, peerID, sleepMode }
interface Metrics { id, timestamp, model, cachedTokens, inputTokens, outputTokens,
                    promptPerSecond, tokensPerSecond, durationMs, hasCapture }
interface ReqRespCapture { id, reqPath, reqHeaders, reqBody, respHeaders, respBody }
interface ChatMessage { role, content: string | ContentPart[], reasoning_content, reasoningTimeMs }
interface ContentPart { type: "text" | "image_url", text?, image_url?: { url } }
```

## Styling Architecture

- CSS custom properties for theming (`--color-primary`, `--color-surface`, etc.)
- Dark mode via `[data-theme="dark"]` attribute
- Component classes: `.card`, `.btn`, `.status--ready`, `.navlink`
- Status badge colors map to ModelStatus values

## File Listing

```
ui-svelte/
├── src/
│   ├── App.svelte, main.ts, index.css
│   ├── components/        (10 files)
│   │   └── playground/    (7 files)
│   ├── lib/               (7 files + 1 test)
│   ├── routes/            (4 files)
│   └── stores/            (3 files)
├── vite.config.ts, svelte.config.js, tsconfig.json
├── package.json, index.html
└── public/                (favicons, manifest)
```
