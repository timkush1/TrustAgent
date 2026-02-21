# TrustAgent React Dashboard

Real-time monitoring dashboard for the TrustAgent hallucination detection system.

## Features

- **Live Audit Feed** - WebSocket connection for real-time updates
- **Trust Score Gauges** - Color-coded grades (A-F) based on faithfulness score
- **Claim Breakdown** - Per-claim verification with evidence display
- **Hallucination Alerts** - Visual badges for detected hallucinations
- **Pipeline Visualization** - Per-step timing for decompose/retrieve/verify/score stages
- **Data Persistence** - Audit history survives page refresh (localStorage)
- **Manual Auditing** - Submit query+response pairs directly from dashboard
- **File Upload** - Drag-and-drop JSON files to expand knowledge base
- **Dark Theme** - Eye-friendly dark UI with Tailwind CSS

## Quick Start

```bash
# Install dependencies
npm install

# Start dev server
npm run dev

# Open http://localhost:5173
```

## Tech Stack

- **React 19** + **TypeScript** - UI framework with type safety
- **Vite** - Lightning-fast build tool and dev server
- **Tailwind CSS** - Utility-first styling
- **Zustand** - Lightweight state management with persistence
- **WebSocket** - Real-time updates from Go proxy

## Project Structure

```
src/
├── main.tsx              # Entry point
├── App.tsx               # Root component with layout
├── components/
│   ├── audit/            # Audit-related components
│   │   ├── AuditFeed.tsx       # Main audit list
│   │   ├── AuditRow.tsx        # Single audit card
│   │   ├── AuditDetail.tsx     # Expanded audit view
│   │   ├── ClaimBreakdown.tsx  # Claim verification list
│   │   ├── PipelineView.tsx    # Pipeline step timing visualization
│   │   └── QueryInput.tsx      # Manual audit submission form
│   ├── upload/
│   │   └── FileUpload.tsx      # File upload component
│   ├── dashboard/
│   │   ├── TrustScoreGauge.tsx # Score gauge (A-F)
│   │   └── MetricsPanel.tsx    # Stats panel
│   └── layout/
│       └── Header.tsx          # Top navigation
├── stores/
│   └── auditStore.ts     # Zustand store with localStorage persistence
├── hooks/
│   └── useWebSocket.ts   # WebSocket connection hook
└── types/
    └── audit.ts          # TypeScript definitions
```

## Components

### AuditFeed
Main audit list component. Displays all audits in reverse chronological order.

**Props:** None (uses Zustand store)

**Features:**
- Auto-scroll to latest audit
- Click to expand detail view
- Persists across page refresh

### AuditDetail
Expanded view for a single audit.

**Props:**
- `audit: Audit` - Audit data

**Features:**
- Trust score gauge
- Pipeline visualization (step timings)
- Claim-by-claim breakdown
- Evidence display
- Reasoning trace

### PipelineView
Horizontal pipeline visualization showing decompose → retrieve → verify → score with per-step timing.

**Props:**
- `stepTimings?: Record<string, number>` - Step execution times in ms

**Features:**
- Color-coded steps (cyan = completed, gray = pending)
- Per-step timing labels
- Total time display

**Example:**
```tsx
<PipelineView stepTimings={{
  decompose_ms: 150,
  retrieve_ms: 800,
  verify_ms: 1200,
  score_ms: 50
}} />
```

### QueryInput
Form for submitting manual audits.

**Props:**
- `apiBaseUrl?: string` - Go proxy URL (default: `http://localhost:8080`)

**Features:**
- Query input
- Response textarea
- Optional model field
- Success/error feedback
- Clears form after submission

**Example:**
```tsx
<QueryInput apiBaseUrl="http://localhost:8080" />
```

### FileUpload
Drag-and-drop file upload for knowledge base documents.

**Props:**
- `apiBaseUrl?: string` - Go proxy URL (default: `http://localhost:8080`)

**Features:**
- Drag-and-drop zone
- Click to browse fallback
- JSON validation
- 10MB size limit
- Success/error feedback
- Document count display

**Example:**
```tsx
<FileUpload apiBaseUrl="http://localhost:8080" />
```

### ClaimBreakdown
List of claims with verification status, confidence, and evidence.

**Props:**
- `claims: Claim[]` - Array of claim objects

**Features:**
- Status badges (SUPPORTED, PARTIALLY_SUPPORTED, UNSUPPORTED)
- Confidence percentage
- Evidence snippets (expandable)
- Color-coded by status

### TrustScoreGauge
Circular gauge showing trust score as a letter grade (A-F).

**Props:**
- `score: number` - Faithfulness score (0-1)

**Grading:**
- A: 90-100%
- B: 80-89%
- C: 70-79%
- D: 60-69%
- F: 0-59%

## State Management

### Zustand Store (`auditStore.ts`)

**State:**
```typescript
{
  audits: Audit[];           // All audits
  selectedAudit: Audit | null;  // Currently expanded audit
  wsConnected: boolean;      // WebSocket connection status
}
```

**Actions:**
- `addAudit(audit: Audit)` - Add new audit to list
- `selectAudit(audit: Audit)` - Expand audit detail
- `setWsConnected(connected: boolean)` - Update connection status

**Persistence:**
- Uses `zustand/middleware/persist`
- Storage: `localStorage` (key: `trustagent-audit-storage`)
- Only `audits` array is persisted (not `selectedAudit` or `wsConnected`)

**Example:**
```typescript
import { useAuditStore } from './stores/auditStore';

function MyComponent() {
  const { audits, addAudit } = useAuditStore();

  // After page refresh, audits array is restored from localStorage
  // selectedAudit and wsConnected reset to defaults
}
```

### WebSocket Hook (`useWebSocket.ts`)

**Usage:**
```typescript
const { connected, audits } = useWebSocket('ws://localhost:8080/ws');
```

**Features:**
- Auto-reconnect on disconnect
- JSON message parsing
- Adds incoming audits to store
- Connection status tracking

## Environment Configuration

Create `.env` file (optional):

```env
VITE_API_URL=http://localhost:8080
VITE_WS_URL=ws://localhost:8080/ws
```

Defaults are set in components if not provided.

## Building for Production

```bash
# Build optimized bundle
npm run build

# Output: dist/

# Preview production build
npm run preview
```

## Development

### Hot Module Replacement
Vite provides instant HMR for React components. Changes reflect immediately.

### TypeScript Type Checking
```bash
npm run type-check
```

### Linting
```bash
npm run lint
```

## API Integration

### WebSocket Message Format

Incoming messages from Go proxy (`ws://localhost:8080/ws`):

```json
{
  "request_id": "abc-123-def-456",
  "query": "What is the capital of France?",
  "response": "London is the capital of France.",
  "model": "llama3.2",
  "trust_score": 0.0,
  "trust_grade": "TRUST_GRADE_D",
  "hallucination_detected": true,
  "claims": [
    {
      "claim": "London is the capital of France",
      "status": "UNSUPPORTED",
      "confidence": 0.95,
      "evidence": ["Paris is the capital of France"]
    }
  ],
  "reasoning_trace": "The claim contradicts the knowledge base...",
  "step_timings": {
    "decompose_ms": 150,
    "retrieve_ms": 800,
    "verify_ms": 1200,
    "score_ms": 50
  },
  "timestamp": "2026-02-15T10:30:00Z"
}
```

### REST API Endpoints

**Submit Manual Audit:**
```typescript
POST http://localhost:8080/api/audit
Content-Type: application/json

{
  "query": "What is gravity?",
  "response": "Gravity is a fundamental force.",
  "model": "test"
}

Response:
{
  "request_id": "xyz-789",
  "status": "submitted"
}
```

**Upload Documents:**
```typescript
POST http://localhost:8080/api/upload
Content-Type: multipart/form-data

Body:
- file: documents.json

Response:
{
  "documents_ingested": 5,
  "status": "success"
}
```

**File Format (JSON):**
```json
[
  {
    "content": "The Eiffel Tower is in Paris, France.",
    "metadata": {"source": "facts.txt", "category": "geography"}
  },
  {
    "content": "Paris is the capital of France.",
    "metadata": {"source": "facts.txt", "category": "geography"}
  }
]
```

## Styling

### Tailwind CSS
All components use Tailwind utility classes. Custom config in `tailwind.config.js`.

### Color Palette
- **Background:** `#0a0a0a`, `#1a1a1a`
- **Borders:** `#374151` (gray-700)
- **Text:** `#e5e7eb` (gray-200)
- **Accent (Trust):** `#06b6d4` (cyan-500)
- **Success:** `#00ff88`
- **Error:** `#ff3366`
- **Warning:** `#ffcc00`

### Trust Score Color Coding
- **A (90-100%):** Green (`#00ff88`)
- **B (80-89%):** Cyan (`#06b6d4`)
- **C (70-79%):** Yellow (`#ffcc00`)
- **D (60-69%):** Orange (`#ff9966`)
- **F (0-59%):** Red (`#ff3366`)

## Testing

### Manual Testing Workflow

1. Start all services (Go proxy, Python engine, Qdrant, Ollama)
2. Start dashboard: `npm run dev`
3. Send test audit via curl or use QueryInput component
4. Verify:
   - WebSocket connection indicator turns green
   - Audit appears in feed
   - Clicking audit shows detail view
   - Refresh page → audit history persists

### E2E Testing

```bash
# From project root
python test_e2e.py

# Should show audits appearing in dashboard WebSocket
```

## Performance

- **Bundle Size:** ~150KB gzipped (production)
- **First Contentful Paint:** <1s
- **WebSocket Latency:** <10ms
- **Audit Render:** <50ms per audit

## Browser Support

- Chrome/Edge 100+
- Firefox 100+
- Safari 15+

WebSocket and localStorage required.

## Troubleshooting

**WebSocket won't connect:**
- Check Go proxy is running on port 8080
- Verify CORS settings in Go proxy
- Check browser console for errors

**Audits not persisting:**
- Check browser localStorage quota
- Verify `localStorage` is enabled
- Check for localStorage errors in console

**File upload fails:**
- Verify file is valid JSON
- Check file size (<10MB)
- Ensure Go proxy `/api/upload` endpoint is accessible

## Related Documentation

- Root README: [../README.md](../README.md)
- Go Proxy: [../backend-go/README.md](../backend-go/README.md)
- Python Audit Engine: [../backend-python/README.md](../backend-python/README.md)
