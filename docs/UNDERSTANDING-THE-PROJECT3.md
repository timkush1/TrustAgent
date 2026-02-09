# 📊 Understanding Phase 3: React Dashboard

> **Document Created**: January 31, 2026  
> **Phase**: 3 - Real-Time Dashboard  
> **Status**: ✅ Complete  
> **Audience**: Junior developers joining the TruthTable project

---

## Table of Contents

1. [Overview](#1-overview)
2. [What Was Built](#2-what-was-built)
3. [Architecture](#3-architecture)
4. [File Structure](#4-file-structure)
5. [Key Components](#5-key-components)
6. [State Management](#6-state-management)
7. [WebSocket Connection](#7-websocket-connection)
8. [Styling & Theme](#8-styling--theme)
9. [How It All Works Together](#9-how-it-all-works-together)
10. [Running the Dashboard](#10-running-the-dashboard)
11. [Testing](#11-testing)
12. [Common Tasks](#12-common-tasks)

---

## 1. Overview

Phase 3 implements the **React Dashboard** - a real-time visualization interface that displays audit results as they stream from the Go proxy via WebSocket. This is the user-facing component of TruthTable that makes the "invisible" audit process visible.

### What Problem Does This Solve?

- **Visibility**: Operators need to see audit results in real-time
- **Trust Monitoring**: Visualize trust scores with intuitive gauges
- **Hallucination Detection**: Instantly see when the AI makes unsupported claims
- **Claim-Level Analysis**: Drill down into individual claim verifications

---

## 2. What Was Built

### Technology Stack

| Technology | Version | Purpose |
|------------|---------|---------|
| React | 19.x | UI framework |
| TypeScript | 5.x | Type safety |
| Vite | 7.3.1 | Build tool & dev server |
| Tailwind CSS | 4.x | Utility-first styling |
| Zustand | 5.x | State management |

### Key Features

1. **Live Audit Feed**: Real-time scrolling list of audit results
2. **Trust Score Gauge**: Animated circular gauge showing overall trust
3. **Claim Breakdown**: Detailed view of each verified claim
4. **Connection Status**: WebSocket connection indicator
5. **Metrics Panel**: Session statistics (total audits, hallucination rate)
6. **Cyberpunk Theme**: Dark mode with neon accents

---

## 3. Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     React Dashboard                         │
│                    http://localhost:5173                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   ┌───────────────┐    ┌───────────────┐    ┌───────────┐  │
│   │   Header      │    │  MetricsPanel │    │  AuditFeed│  │
│   │  (status)     │    │  (gauges)     │    │  (list)   │  │
│   └───────────────┘    └───────────────┘    └───────────┘  │
│           │                    │                  │         │
│           └────────────────────┴──────────────────┘         │
│                          │                                  │
│                  ┌───────┴───────┐                         │
│                  │ Zustand Store  │                         │
│                  │  (auditStore)  │                         │
│                  └───────┬───────┘                         │
│                          │                                  │
│                  ┌───────┴───────┐                         │
│                  │ useWebSocket  │                         │
│                  │    Hook       │                         │
│                  └───────┬───────┘                         │
│                          │                                  │
└──────────────────────────┼──────────────────────────────────┘
                           │ WebSocket
                           ▼
              ┌────────────────────────┐
              │     Go Proxy           │
              │   ws://localhost:8081  │
              └────────────────────────┘
```

### Data Flow

1. **Go Proxy** broadcasts audit results via WebSocket
2. **useWebSocket hook** receives messages and parses JSON
3. **AuditFeed component** listens for messages, adds to **Zustand store**
4. **All components** reactively update based on store state

---

## 4. File Structure

```
frontend-react/
├── src/
│   ├── main.tsx                    # React entry point
│   ├── App.tsx                     # Main layout component
│   ├── index.css                   # Global styles + Tailwind
│   │
│   ├── types/
│   │   └── audit.ts                # TypeScript types for audit data
│   │
│   ├── hooks/
│   │   └── useWebSocket.ts         # WebSocket connection hook
│   │
│   ├── stores/
│   │   └── auditStore.ts           # Zustand state management
│   │
│   └── components/
│       ├── layout/
│       │   └── Header.tsx          # Top navigation bar
│       │
│       ├── dashboard/
│       │   ├── MetricsPanel.tsx    # Stats cards + gauge
│       │   └── TrustScoreGauge.tsx # Animated circular gauge
│       │
│       └── audit/
│           ├── AuditFeed.tsx       # Main audit list container
│           ├── AuditRow.tsx        # Single audit list item
│           ├── AuditDetail.tsx     # Modal with full details
│           └── ClaimBreakdown.tsx  # List of verified claims
│
├── vite.config.ts                  # Vite + proxy config
├── tailwind.config.js              # Tailwind (via Vite plugin)
├── tsconfig.json                   # TypeScript config
└── package.json                    # Dependencies
```

---

## 5. Key Components

### 5.1 TrustScoreGauge

A circular SVG gauge that visualizes the trust score (0-100%).

```tsx
// Location: src/components/dashboard/TrustScoreGauge.tsx

<TrustScoreGauge 
  score={0.85}      // 0.0 - 1.0
  size="md"         // 'sm' | 'md' | 'lg'
  showLabel={true}  // Show "TRUST" label
  animated={true}   // Animate on change
/>
```

**Color mapping:**
- 🟢 Green (≥80%): Highly trustworthy
- 🟡 Yellow (60-79%): Some concerns
- 🟠 Orange (40-59%): Significant issues
- 🔴 Red (<40%): Major hallucinations

### 5.2 AuditFeed

The main scrolling list of audit results with live updates.

```tsx
// Location: src/components/audit/AuditFeed.tsx

// Automatically receives WebSocket messages and updates
<AuditFeed />
```

**Features:**
- Auto-scrolls to new entries
- Shows connection status indicator
- Click any row to open detail view
- Displays hallucination badge for flagged audits

### 5.3 ClaimBreakdown

Displays each claim extracted from the LLM response with its verification status.

```tsx
// Location: src/components/audit/ClaimBreakdown.tsx

<ClaimBreakdown claims={[
  {
    claim: "Paris is the capital of France",
    status: "SUPPORTED",
    confidence: 0.95,
    evidence: ["France's capital is Paris..."]
  },
  {
    claim: "The Eiffel Tower is 500 meters tall",
    status: "UNSUPPORTED",
    confidence: 0.88,
    evidence: []
  }
]} />
```

**Status indicators:**
- ✓ SUPPORTED - Green, verified by context
- ✗ UNSUPPORTED - Red, hallucination detected
- ~ PARTIALLY_SUPPORTED - Yellow, partially verified
- ? UNKNOWN - Gray, couldn't determine

---

## 6. State Management

We use **Zustand** for simple, powerful state management.

```typescript
// Location: src/stores/auditStore.ts

interface AuditState {
  audits: AuditResult[];           // List of all audits (max 100)
  selectedAudit: AuditResult | null;  // Currently selected for detail view
  
  // Actions
  addAudit: (audit: AuditResult) => void;
  selectAudit: (auditId: string | null) => void;
  clearAudits: () => void;
  
  // Computed
  getStats: () => { total, avgScore, hallucinationCount, hallucinationRate };
}
```

### Using the Store

```tsx
// In any component:
import { useAuditStore } from '../stores/auditStore';

function MyComponent() {
  // Get state
  const audits = useAuditStore((state) => state.audits);
  const selectedAudit = useAuditStore((state) => state.selectedAudit);
  
  // Get actions
  const addAudit = useAuditStore((state) => state.addAudit);
  const selectAudit = useAuditStore((state) => state.selectAudit);
  
  // Get computed stats
  const getStats = useAuditStore((state) => state.getStats);
  const stats = getStats();
  
  return <div>Total: {stats.total}</div>;
}
```

---

## 7. WebSocket Connection

### The useWebSocket Hook

```typescript
// Location: src/hooks/useWebSocket.ts

const { status, lastMessage, send, reconnect } = useWebSocket();

// status: { connected: boolean, lastConnected?: Date, error?: string }
// lastMessage: WSMessage | null
// send: (data: object) => void
// reconnect: () => void
```

### Connection Features

1. **Auto-connect on mount**: Connects when component mounts
2. **Auto-reconnect**: Retries up to 10 times with 3s delay
3. **Connection status**: Exposed via `status` object
4. **JSON parsing**: Automatically parses incoming messages

### Message Types

```typescript
// Location: src/types/audit.ts

interface WSMessage {
  type: 'audit_result' | 'metric_update' | 'error' | 'pong' | 'connected';
  timestamp: string;
  data?: AuditResult | MetricUpdate | ErrorPayload;
}
```

---

## 8. Styling & Theme

### Cyberpunk Dark Theme

We use CSS custom properties for consistent theming:

```css
/* Location: src/index.css */

:root {
  --bg-primary: #0a0a0f;      /* Main background */
  --bg-secondary: #12121a;    /* Cards, panels */
  --bg-tertiary: #1a1a24;     /* Hover states */
  --text-primary: #e0e0e0;    /* Main text */
  --text-secondary: #888;      /* Muted text */
  --accent-cyan: #00ffff;      /* Primary accent */
  --accent-magenta: #ff00ff;   /* Secondary accent */
  --accent-green: #00ff88;     /* Success */
  --accent-red: #ff3366;       /* Error/Hallucination */
  --accent-yellow: #ffcc00;    /* Warning */
  --border-color: #2a2a3a;     /* Borders */
}
```

### Glow Effects

```css
.glow-cyan {
  box-shadow: 0 0 10px var(--accent-cyan), 
              0 0 20px rgba(0, 255, 255, 0.3);
}

.glow-green {
  box-shadow: 0 0 10px var(--accent-green), 
              0 0 20px rgba(0, 255, 136, 0.3);
}
```

### Tailwind Integration

Tailwind is configured via the Vite plugin:

```typescript
// vite.config.ts
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // ...
})
```

---

## 9. How It All Works Together

### Startup Sequence

1. **Vite dev server starts** → `npm run dev`
2. **React app loads** → `main.tsx` mounts `<App />`
3. **useWebSocket hook connects** → Connects to `ws://localhost:8081/ws`
4. **Connection established** → Status indicator turns green

### Audit Flow

```
User Query → Go Proxy → LLM Response
                ↓
        Async Audit Dispatch
                ↓
        Python Audit Engine
                ↓
        AuditResult → Redis PubSub
                ↓
        Go Proxy WebSocket Hub
                ↓
        Dashboard receives message
                ↓
        auditStore.addAudit()
                ↓
        UI updates reactively
```

### Click Flow (Viewing Details)

1. User clicks on audit row in `AuditFeed`
2. `selectAudit(auditId)` called on store
3. `selectedAudit` state updates
4. `AuditDetail` modal renders with full data
5. User sees claims, response, reasoning trace
6. User clicks X → `selectAudit(null)` → modal closes

---

## 10. Running the Dashboard

### Prerequisites

```bash
# Ensure these are running:
# 1. Docker services (Redis, Qdrant, Ollama)
docker-compose up -d

# 2. Python Audit Engine
cd backend-python
source .venv/bin/activate
python -m truthtable.main

# 3. Go Proxy
cd backend-go
go run ./cmd/proxy
```

### Start the Dashboard

```bash
cd frontend-react

# Install dependencies (first time only)
npm install

# Start dev server
npm run dev

# Opens at http://localhost:5173
```

### Build for Production

```bash
npm run build
# Output in dist/
```

---

## 11. Testing

### Type Checking

```bash
# Check for TypeScript errors
npx tsc --noEmit
```

### Manual Testing

1. Open dashboard at `http://localhost:5173`
2. Check WebSocket status indicator (should be green)
3. Send a request through the proxy:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "What is the capital of France?"}]
  }'
```

4. Watch the audit appear in the feed
5. Click to view details

---

## 12. Common Tasks

### Adding a New Component

1. Create file in appropriate directory:
   ```
   src/components/{category}/{ComponentName}.tsx
   ```

2. Use the pattern:
   ```tsx
   interface Props {
     // Define props
   }
   
   export function ComponentName({ prop1, prop2 }: Props) {
     return (
       <div className="...">
         {/* Content */}
       </div>
     );
   }
   ```

3. Import and use in parent component

### Adding New State

1. Update `src/stores/auditStore.ts`:
   ```typescript
   interface AuditState {
     // Add new state
     newField: string;
     
     // Add action
     setNewField: (value: string) => void;
   }
   
   export const useAuditStore = create<AuditState>((set) => ({
     newField: '',
     setNewField: (value) => set({ newField: value }),
   }));
   ```

### Handling New WebSocket Message Types

1. Add type to `src/types/audit.ts`
2. Handle in `AuditFeed.tsx` useEffect:
   ```typescript
   useEffect(() => {
     if (lastMessage?.type === 'new_message_type') {
       // Handle new message
     }
   }, [lastMessage]);
   ```

### Changing Colors/Theme

Edit `src/index.css` CSS custom properties:
```css
:root {
  --accent-cyan: #your-color;
}
```

---

## Summary

Phase 3 delivers a complete real-time dashboard for monitoring AI audits:

- ✅ **Vite + React + TypeScript** - Modern, type-safe frontend
- ✅ **Tailwind CSS** - Cyberpunk dark theme with glow effects
- ✅ **Zustand** - Simple state management
- ✅ **WebSocket** - Real-time updates from Go proxy
- ✅ **Trust Score Gauge** - Visual trust indicator
- ✅ **Claim Breakdown** - Detailed verification view
- ✅ **Responsive Layout** - Sidebar + main content

The dashboard runs at `http://localhost:5173` (or 5174/5175 if port busy) and connects to the Go proxy WebSocket at `ws://localhost:8081/ws`.

---

## 13. Understanding Audit Results

### Why Does "2+2=4" Show as Hallucination?

If you send this test:
```json
{
  "test_response": "2+2 equals 4. This was first discovered by Albert Einstein in 1905."
}
```

The system will show **~50% faithfulness** because:

| Claim | Status | Why |
|-------|--------|-----|
| "2+2 equals 4" | ✅ SUPPORTED | Factually correct |
| "This was first discovered by Albert Einstein in 1905" | ❌ UNSUPPORTED | **Hallucination!** Einstein didn't discover 2+2=4 |

**The system is working correctly!** It detected the false historical claim.

### Score Interpretation

| Score | Meaning |
|-------|---------|
| 90-100% | Highly trustworthy, all claims verified |
| 70-89% | Mostly accurate, minor issues |
| 50-69% | Mixed accuracy, some hallucinations |
| <50% | Major hallucinations detected |

---

*Document version: 1.1*  
*Last updated: January 31, 2026*
