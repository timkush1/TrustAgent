# Phase 3: React Dashboard - Complete Guide

> **Status**: ✅ Complete and Working  
> **Port**: 5173-5175 (Vite dev server)  
> **Stack**: React 19 + TypeScript + Vite 7 + Tailwind CSS 4 + Zustand 5  
> **Audience**: Junior developers learning the codebase

---

## Table of Contents

1. [Overview](#1-overview)
2. [What This Component Does](#2-what-this-component-does)
3. [Architecture](#3-architecture)
4. [Directory Structure](#4-directory-structure)
5. [File-by-File Explanation](#5-file-by-file-explanation)
6. [Data Flow](#6-data-flow)
7. [State Management with Zustand](#7-state-management-with-zustand)
8. [WebSocket Connection](#8-websocket-connection)
9. [Styling with Tailwind](#9-styling-with-tailwind)
10. [Configuration](#10-configuration)
11. [Running the Dashboard](#11-running-the-dashboard)
12. [Testing](#12-testing)
13. [Common Tasks](#13-common-tasks)
14. [Troubleshooting](#14-troubleshooting)

---

## 1. Overview

The React Dashboard is the **monitoring interface** for TruthTable. It displays real-time audit results as they come in via WebSocket.

### Key Technologies

| Technology | Version | Purpose |
|------------|---------|---------|
| React | 19.x | UI framework |
| TypeScript | 5.x | Type safety |
| Vite | 7.3.1 | Build tool & dev server |
| Zustand | 5.x | State management |
| Tailwind CSS | 4.x | Styling |
| Lucide React | - | Icons |

### Why This Stack?

- **React 19**: Latest features, concurrent rendering
- **Vite**: Blazing fast hot reload (vs Webpack)
- **Zustand**: Simpler than Redux, perfect for this scale
- **Tailwind 4**: Utility-first CSS, rapid development
- **TypeScript**: Catch bugs at compile time

---

## 2. What This Component Does

### The Dashboard in One Sentence

Connects to Go Proxy via WebSocket, receives audit results in real-time, and displays them in a clean UI with color-coded trust scores.

### Key Features

| Feature | Description |
|---------|-------------|
| **Real-time updates** | WebSocket receives results instantly |
| **Trust score display** | Color-coded (green/yellow/red) |
| **Claim breakdown** | Show each claim with verification |
| **Request/Response view** | Full text of original interaction |
| **Connection status** | Shows WebSocket connection state |

### Visual Flow

```
┌──────────────────────────────────────────────────────────────────┐
│                        Dashboard UI                               │
│ ┌────────────────────────────────────────────────────────────┐   │
│ │  Header: TruthTable Dashboard          🟢 Connected        │   │
│ └────────────────────────────────────────────────────────────┘   │
│                                                                   │
│ ┌────────────────────────────────────────────────────────────┐   │
│ │  Audit Results                                   [Clear]   │   │
│ │                                                            │   │
│ │  ┌────────────────────────────────────────────────────┐   │   │
│ │  │ Request #abc123                           51% 🟡  │   │   │
│ │  │ Model: gpt-4o                                     │   │   │
│ │  │ ────────────────────────────────────────────────  │   │   │
│ │  │ Claims:                                           │   │   │
│ │  │   ✅ "2+2 equals 4"                  SUPPORTED    │   │   │
│ │  │   ❌ "Discovered by Einstein"        UNSUPPORTED  │   │   │
│ │  └────────────────────────────────────────────────────┘   │   │
│ │                                                            │   │
│ │  ┌────────────────────────────────────────────────────┐   │   │
│ │  │ Request #def456                           92% 🟢  │   │   │
│ │  │ ...                                               │   │   │
│ │  └────────────────────────────────────────────────────┘   │   │
│ └────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

---

## 3. Architecture

```
┌────────────────────────────────────────────────────────────────┐
│                      React Dashboard                            │
│                                                                 │
│   ┌─────────────────────────────────────────────────────────┐  │
│   │                      App.tsx                             │  │
│   │  ┌──────────────────────────────────────────────────┐   │  │
│   │  │                Header.tsx                         │   │  │
│   │  │            Connection Status                      │   │  │
│   │  └──────────────────────────────────────────────────┘   │  │
│   │                                                          │  │
│   │  ┌──────────────────────────────────────────────────┐   │  │
│   │  │               AuditResults.tsx                    │   │  │
│   │  │  ┌────────────────────────────────────────────┐  │   │  │
│   │  │  │           AuditCard.tsx                    │  │   │  │
│   │  │  │  ┌─────────────────────────────────────┐   │  │   │  │
│   │  │  │  │         ClaimItem.tsx               │   │  │   │  │
│   │  │  │  └─────────────────────────────────────┘   │  │   │  │
│   │  │  └────────────────────────────────────────────┘  │   │  │
│   │  └──────────────────────────────────────────────────┘   │  │
│   └─────────────────────────────────────────────────────────┘  │
│                              │                                  │
│                              ▼                                  │
│   ┌─────────────────────────────────────────────────────────┐  │
│   │                    Zustand Store                         │  │
│   │              (useAuditStore.ts)                         │  │
│   │   results: AuditResult[]                                │  │
│   │   addResult(), clearResults()                           │  │
│   └─────────────────────────────────────────────────────────┘  │
│                              ▲                                  │
│                              │                                  │
│   ┌─────────────────────────────────────────────────────────┐  │
│   │                   useWebSocket Hook                      │  │
│   │              (useWebSocket.ts)                          │  │
│   │   Connected to ws://localhost:8081/ws                   │  │
│   │   onMessage → addResult()                               │  │
│   └─────────────────────────────────────────────────────────┘  │
│                              ▲                                  │
└──────────────────────────────│──────────────────────────────────┘
                               │
                               │ WebSocket
                               │
                    ┌──────────┴──────────┐
                    │    Go Proxy         │
                    │    (Port 8081)      │
                    └─────────────────────┘
```

---

## 4. Directory Structure

```
frontend-react/
├── src/
│   ├── main.tsx                 # Entry point - mounts React app
│   ├── App.tsx                  # Root component - assembles UI
│   │
│   ├── components/              # UI Components
│   │   ├── Header.tsx           # Top bar with title & status
│   │   ├── ConnectionStatus.tsx # WebSocket status indicator
│   │   ├── AuditResults.tsx     # Container for result cards
│   │   ├── AuditCard.tsx        # Single audit result display
│   │   ├── ClaimItem.tsx        # Individual claim with verdict
│   │   └── TrustScore.tsx       # Colored score badge
│   │
│   ├── hooks/                   # Custom React Hooks
│   │   └── useWebSocket.ts      # WebSocket connection manager
│   │
│   ├── stores/                  # Zustand State Stores
│   │   └── useAuditStore.ts     # Audit results state
│   │
│   ├── types/                   # TypeScript Types
│   │   └── audit.ts             # AuditResult, Claim, etc.
│   │
│   └── index.css                # Tailwind CSS imports
│
├── index.html                   # HTML template
├── vite.config.ts               # Vite configuration
├── tailwind.config.js           # Tailwind configuration
├── tsconfig.json                # TypeScript configuration
└── package.json                 # Dependencies & scripts
```

---

## 5. File-by-File Explanation

### 5.1 Entry Point: `src/main.tsx`

**Location**: `src/main.tsx`

**Purpose**: The very first file that runs. Mounts React to the DOM.

```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
```

**What each line does**:
1. Import React library
2. Import ReactDOM for mounting
3. Import our main App component
4. Import global CSS (Tailwind)
5. Find the `root` div in index.html
6. Mount our App inside StrictMode (helps catch bugs)

---

### 5.2 Root Component: `src/App.tsx`

**Location**: `src/App.tsx`

**Purpose**: Assembles all components and initializes WebSocket.

```tsx
import { Header } from './components/Header'
import { AuditResults } from './components/AuditResults'
import { useWebSocket } from './hooks/useWebSocket'

function App() {
  // Initialize WebSocket connection
  const { isConnected, error } = useWebSocket('ws://localhost:8081/ws')

  return (
    <div className="min-h-screen bg-gray-900 text-white">
      <Header isConnected={isConnected} />
      
      {error && (
        <div className="bg-red-600 text-white p-4">
          WebSocket Error: {error}
        </div>
      )}
      
      <main className="container mx-auto p-4">
        <AuditResults />
      </main>
    </div>
  )
}

export default App
```

**What it does**:
1. Calls `useWebSocket` hook to connect
2. Renders Header with connection status
3. Shows error banner if connection fails
4. Renders AuditResults container

---

### 5.3 Types: `src/types/audit.ts`

**Location**: `src/types/audit.ts`

**Purpose**: TypeScript type definitions for all data structures.

```typescript
// A single claim extracted from the response
export interface ClaimVerification {
  claim: string           // The statement text
  verdict: 'SUPPORTED' | 'UNSUPPORTED' | 'UNKNOWN'
  confidence: number      // 0.0 to 1.0
  evidence?: string       // Supporting evidence (if any)
}

// Complete audit result from Python engine
export interface AuditResult {
  request_id: string      // Unique ID for this request
  prompt: string          // User's original question
  response: string        // LLM's response text
  model: string           // Model name (gpt-4o, llama3, etc.)
  timestamp: string       // ISO 8601 timestamp
  
  // Scores (0-100)
  overall_score: number
  faithfulness_score: number
  
  // Claim breakdown
  claims: ClaimVerification[]
  
  // Status
  status: 'pending' | 'completed' | 'error'
  error?: string
}

// WebSocket message wrapper
export interface WSMessage {
  type: 'connected' | 'audit_result' | 'error'
  timestamp: string
  data?: AuditResult
}
```

---

### 5.4 Zustand Store: `src/stores/useAuditStore.ts`

**Location**: `src/stores/useAuditStore.ts`

**Purpose**: Global state management for audit results.

**Why Zustand?**
- Simpler than Redux (no actions, reducers, dispatch)
- TypeScript-friendly
- Small bundle size
- Works outside of React components

```typescript
import { create } from 'zustand'
import type { AuditResult } from '../types/audit'

interface AuditStore {
  // State
  results: AuditResult[]
  
  // Actions
  addResult: (result: AuditResult) => void
  clearResults: () => void
}

export const useAuditStore = create<AuditStore>((set) => ({
  results: [],
  
  addResult: (result) => set((state) => ({
    // Add new result at the beginning (newest first)
    results: [result, ...state.results]
  })),
  
  clearResults: () => set({ results: [] }),
}))
```

**Usage in components**:
```tsx
// Read results
const results = useAuditStore((state) => state.results)

// Add a result
const addResult = useAuditStore((state) => state.addResult)
addResult(newResult)

// Clear all
const clearResults = useAuditStore((state) => state.clearResults)
clearResults()
```

---

### 5.5 WebSocket Hook: `src/hooks/useWebSocket.ts`

**Location**: `src/hooks/useWebSocket.ts`

**Purpose**: Manages WebSocket connection lifecycle.

```typescript
import { useEffect, useState, useRef } from 'react'
import { useAuditStore } from '../stores/useAuditStore'
import type { WSMessage } from '../types/audit'

export function useWebSocket(url: string) {
  const [isConnected, setIsConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const addResult = useAuditStore((state) => state.addResult)

  useEffect(() => {
    // Create WebSocket connection
    const ws = new WebSocket(url)
    wsRef.current = ws

    // Connection opened
    ws.onopen = () => {
      console.log('WebSocket connected')
      setIsConnected(true)
      setError(null)
    }

    // Message received
    ws.onmessage = (event) => {
      const message: WSMessage = JSON.parse(event.data)
      
      if (message.type === 'audit_result' && message.data) {
        addResult(message.data)
      }
    }

    // Connection closed
    ws.onclose = () => {
      console.log('WebSocket disconnected')
      setIsConnected(false)
    }

    // Connection error
    ws.onerror = (e) => {
      console.error('WebSocket error:', e)
      setError('Connection failed')
      setIsConnected(false)
    }

    // Cleanup on unmount
    return () => {
      ws.close()
    }
  }, [url, addResult])

  return { isConnected, error }
}
```

**Key points**:
1. Opens connection on mount
2. Parses incoming JSON messages
3. Calls `addResult` for audit results
4. Handles reconnection (could be enhanced)
5. Cleans up on unmount

---

### 5.6 Component: `src/components/Header.tsx`

**Location**: `src/components/Header.tsx`

**Purpose**: Top navigation bar with title and connection status.

```tsx
import { ConnectionStatus } from './ConnectionStatus'

interface HeaderProps {
  isConnected: boolean
}

export function Header({ isConnected }: HeaderProps) {
  return (
    <header className="bg-gray-800 border-b border-gray-700 px-6 py-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-white">
          TruthTable Dashboard
        </h1>
        <ConnectionStatus isConnected={isConnected} />
      </div>
    </header>
  )
}
```

---

### 5.7 Component: `src/components/ConnectionStatus.tsx`

**Location**: `src/components/ConnectionStatus.tsx`

**Purpose**: Visual indicator of WebSocket connection state.

```tsx
interface ConnectionStatusProps {
  isConnected: boolean
}

export function ConnectionStatus({ isConnected }: ConnectionStatusProps) {
  return (
    <div className="flex items-center gap-2">
      <div
        className={`w-3 h-3 rounded-full ${
          isConnected ? 'bg-green-500' : 'bg-red-500'
        }`}
      />
      <span className="text-sm text-gray-400">
        {isConnected ? 'Connected' : 'Disconnected'}
      </span>
    </div>
  )
}
```

**Visual states**:
- 🟢 Green dot + "Connected" = WebSocket open
- 🔴 Red dot + "Disconnected" = WebSocket closed

---

### 5.8 Component: `src/components/AuditResults.tsx`

**Location**: `src/components/AuditResults.tsx`

**Purpose**: Container that displays all audit result cards.

```tsx
import { useAuditStore } from '../stores/useAuditStore'
import { AuditCard } from './AuditCard'

export function AuditResults() {
  const results = useAuditStore((state) => state.results)
  const clearResults = useAuditStore((state) => state.clearResults)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold">Audit Results</h2>
        {results.length > 0 && (
          <button
            onClick={clearResults}
            className="px-3 py-1 bg-gray-700 hover:bg-gray-600 rounded text-sm"
          >
            Clear All
          </button>
        )}
      </div>

      {results.length === 0 ? (
        <div className="text-center text-gray-500 py-8">
          No audit results yet. Send a request through the proxy.
        </div>
      ) : (
        <div className="space-y-4">
          {results.map((result) => (
            <AuditCard key={result.request_id} result={result} />
          ))}
        </div>
      )}
    </div>
  )
}
```

---

### 5.9 Component: `src/components/AuditCard.tsx`

**Location**: `src/components/AuditCard.tsx`

**Purpose**: Displays a single audit result with all details.

```tsx
import { TrustScore } from './TrustScore'
import { ClaimItem } from './ClaimItem'
import type { AuditResult } from '../types/audit'

interface AuditCardProps {
  result: AuditResult
}

export function AuditCard({ result }: AuditCardProps) {
  return (
    <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div>
          <span className="text-gray-400 text-sm">
            Request #{result.request_id.slice(0, 8)}
          </span>
          <span className="text-gray-600 text-sm ml-4">
            {result.model}
          </span>
        </div>
        <TrustScore score={result.overall_score} />
      </div>

      {/* Prompt */}
      <div className="mb-4">
        <h4 className="text-sm font-medium text-gray-400">Prompt</h4>
        <p className="text-gray-300 bg-gray-900 p-2 rounded mt-1">
          {result.prompt}
        </p>
      </div>

      {/* Response */}
      <div className="mb-4">
        <h4 className="text-sm font-medium text-gray-400">Response</h4>
        <p className="text-gray-300 bg-gray-900 p-2 rounded mt-1">
          {result.response}
        </p>
      </div>

      {/* Claims */}
      <div>
        <h4 className="text-sm font-medium text-gray-400 mb-2">
          Claims ({result.claims?.length || 0})
        </h4>
        <div className="space-y-2">
          {result.claims?.map((claim, index) => (
            <ClaimItem key={index} claim={claim} />
          ))}
        </div>
      </div>
    </div>
  )
}
```

---

### 5.10 Component: `src/components/TrustScore.tsx`

**Location**: `src/components/TrustScore.tsx`

**Purpose**: Color-coded score badge.

```tsx
interface TrustScoreProps {
  score: number  // 0-100
}

export function TrustScore({ score }: TrustScoreProps) {
  // Determine color based on score
  const getColorClass = () => {
    if (score >= 80) return 'bg-green-600 text-white'
    if (score >= 60) return 'bg-yellow-600 text-white'
    return 'bg-red-600 text-white'
  }

  return (
    <div className={`px-3 py-1 rounded-full text-sm font-medium ${getColorClass()}`}>
      {score}%
    </div>
  )
}
```

**Score colors**:
| Score Range | Color | Meaning |
|-------------|-------|---------|
| 80-100% | 🟢 Green | High trust |
| 60-79% | 🟡 Yellow | Medium trust |
| 0-59% | 🔴 Red | Low trust |

---

### 5.11 Component: `src/components/ClaimItem.tsx`

**Location**: `src/components/ClaimItem.tsx`

**Purpose**: Displays a single claim with its verification verdict.

```tsx
import { CheckCircle, XCircle, HelpCircle } from 'lucide-react'
import type { ClaimVerification } from '../types/audit'

interface ClaimItemProps {
  claim: ClaimVerification
}

export function ClaimItem({ claim }: ClaimItemProps) {
  const getIcon = () => {
    switch (claim.verdict) {
      case 'SUPPORTED':
        return <CheckCircle className="w-5 h-5 text-green-500" />
      case 'UNSUPPORTED':
        return <XCircle className="w-5 h-5 text-red-500" />
      default:
        return <HelpCircle className="w-5 h-5 text-gray-500" />
    }
  }

  const getVerdictColor = () => {
    switch (claim.verdict) {
      case 'SUPPORTED':
        return 'text-green-400'
      case 'UNSUPPORTED':
        return 'text-red-400'
      default:
        return 'text-gray-400'
    }
  }

  return (
    <div className="flex items-start gap-3 bg-gray-900 p-3 rounded">
      {getIcon()}
      <div className="flex-1">
        <p className="text-gray-300">{claim.claim}</p>
        <span className={`text-xs ${getVerdictColor()}`}>
          {claim.verdict} ({Math.round(claim.confidence * 100)}%)
        </span>
      </div>
    </div>
  )
}
```

---

## 6. Data Flow

### Complete Data Flow Diagram

```
┌───────────────────────────────────────────────────────────────┐
│                     React Dashboard                            │
│                                                                │
│   1. useWebSocket connects to ws://localhost:8081/ws          │
│      │                                                         │
│      ▼                                                         │
│   2. WebSocket receives message:                               │
│      {                                                         │
│        "type": "audit_result",                                 │
│        "data": { request_id, score, claims... }               │
│      }                                                         │
│      │                                                         │
│      ▼                                                         │
│   3. Hook calls addResult(data)                                │
│      │                                                         │
│      ▼                                                         │
│   4. Zustand store updates:                                    │
│      results = [newResult, ...oldResults]                      │
│      │                                                         │
│      ▼                                                         │
│   5. React re-renders AuditResults                             │
│      │                                                         │
│      ▼                                                         │
│   6. AuditCard displays new result                             │
│                                                                │
└───────────────────────────────────────────────────────────────┘
```

### Message Format from Go Proxy

```json
{
  "type": "audit_result",
  "timestamp": "2024-01-15T10:30:00Z",
  "data": {
    "request_id": "abc123",
    "prompt": "What is 2+2?",
    "response": "2+2 equals 4.",
    "model": "gpt-4o",
    "overall_score": 100,
    "faithfulness_score": 100,
    "claims": [
      {
        "claim": "2+2 equals 4",
        "verdict": "SUPPORTED",
        "confidence": 0.95
      }
    ],
    "status": "completed"
  }
}
```

---

## 7. State Management with Zustand

### Why Zustand Over Redux?

| Feature | Redux | Zustand |
|---------|-------|---------|
| Boilerplate | High (actions, reducers) | Low (just functions) |
| Learning curve | Steep | Gentle |
| Bundle size | ~5KB | ~1KB |
| DevTools | Yes | Yes |
| Outside React | Possible | Easy |

### Our Store Pattern

```typescript
// 1. Define types
interface AuditStore {
  results: AuditResult[]
  addResult: (result: AuditResult) => void
  clearResults: () => void
}

// 2. Create store
export const useAuditStore = create<AuditStore>((set) => ({
  // Initial state
  results: [],
  
  // Actions (mutations)
  addResult: (result) => set((state) => ({
    results: [result, ...state.results]
  })),
  
  clearResults: () => set({ results: [] }),
}))
```

### Using the Store

```tsx
// In any component
function MyComponent() {
  // Subscribe to specific slice (optimized re-renders)
  const results = useAuditStore((state) => state.results)
  
  // Get action
  const addResult = useAuditStore((state) => state.addResult)
  
  // Use them
  console.log(results.length)
  addResult(newResult)
}
```

---

## 8. WebSocket Connection

### Connection Lifecycle

```
Mount                    Connected                   Unmount
  │                          │                          │
  ▼                          ▼                          ▼
new WebSocket()  ──────▶  ws.onopen  ──────▶       ws.close()
                              │
                              ▼
                         ws.onmessage
                              │
                              ▼
                         addResult()
```

### Reconnection Strategy

Our current implementation doesn't auto-reconnect. To add this:

```typescript
// Enhanced useWebSocket with reconnection
useEffect(() => {
  let ws: WebSocket
  let reconnectTimer: number
  
  const connect = () => {
    ws = new WebSocket(url)
    
    ws.onclose = () => {
      // Reconnect after 3 seconds
      reconnectTimer = setTimeout(connect, 3000)
    }
    
    ws.onmessage = handleMessage
  }
  
  connect()
  
  return () => {
    clearTimeout(reconnectTimer)
    ws.close()
  }
}, [url])
```

---

## 9. Styling with Tailwind

### Tailwind v4 Setup

In `tailwind.config.js`:
```javascript
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {},
  },
  plugins: [],
}
```

In `src/index.css`:
```css
@import "tailwindcss";
```

### Common Patterns We Use

| Pattern | Tailwind Classes |
|---------|------------------|
| Dark background | `bg-gray-900` |
| Card | `bg-gray-800 rounded-lg p-4 border border-gray-700` |
| Green success | `text-green-500`, `bg-green-600` |
| Red error | `text-red-500`, `bg-red-600` |
| Flex layout | `flex items-center justify-between` |
| Spacing | `space-y-4`, `gap-2`, `p-4`, `mx-auto` |

### Responsive Design

```tsx
// Mobile-first approach
<div className="
  w-full              // Default: full width
  md:w-1/2            // Medium screens: half width
  lg:w-1/3            // Large screens: one-third width
">
```

---

## 10. Configuration

### Vite Configuration (`vite.config.ts`)

```typescript
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    open: true,  // Open browser on start
  },
})
```

### TypeScript Configuration (`tsconfig.json`)

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "strict": true,
    "jsx": "react-jsx",
    "moduleResolution": "bundler"
  }
}
```

### Environment Variables

Create `.env` file:
```bash
VITE_WS_URL=ws://localhost:8081/ws
```

Use in code:
```typescript
const wsUrl = import.meta.env.VITE_WS_URL || 'ws://localhost:8081/ws'
```

---

## 11. Running the Dashboard

### Development Mode

```bash
cd frontend-react

# Install dependencies
npm install

# Start dev server
npm run dev
```

Expected output:
```
  VITE v7.3.1  ready in 234 ms

  ➜  Local:   http://localhost:5173/
  ➜  Network: use --host to expose
  ➜  press h + enter to show help
```

### Production Build

```bash
# Build
npm run build

# Preview production build
npm run preview
```

### Verify It's Working

1. Open http://localhost:5173 in browser
2. Check console for "WebSocket connected"
3. Send a request through the proxy
4. See result appear in dashboard

---

## 12. Testing

### Run Tests

```bash
cd frontend-react
npm test
```

### Manual Testing

1. **Test WebSocket connection**:
   - Start Go proxy on port 8081
   - Open dashboard
   - Check for green "Connected" status

2. **Test audit display**:
   - Send request via curl to Go proxy
   - Watch result appear in dashboard

3. **Test clear button**:
   - Click "Clear All"
   - Verify results disappear

### Component Testing Example

```typescript
// AuditCard.test.tsx
import { render, screen } from '@testing-library/react'
import { AuditCard } from './AuditCard'

test('displays score correctly', () => {
  const result = {
    request_id: 'abc123',
    overall_score: 75,
    // ... other fields
  }
  
  render(<AuditCard result={result} />)
  
  expect(screen.getByText('75%')).toBeInTheDocument()
})
```

---

## 13. Common Tasks

### Adding a New Field to Display

1. Update type in `src/types/audit.ts`:
```typescript
export interface AuditResult {
  // ... existing fields
  newField: string  // Add here
}
```

2. Display in `src/components/AuditCard.tsx`:
```tsx
<div>
  <h4>New Field</h4>
  <p>{result.newField}</p>
</div>
```

### Creating a New Component

1. Create file `src/components/MyComponent.tsx`:
```tsx
interface MyComponentProps {
  value: string
}

export function MyComponent({ value }: MyComponentProps) {
  return <div className="...">{value}</div>
}
```

2. Import and use:
```tsx
import { MyComponent } from './components/MyComponent'

<MyComponent value="hello" />
```

### Adding to Zustand Store

```typescript
// In useAuditStore.ts
interface AuditStore {
  // ... existing
  selectedId: string | null
  setSelectedId: (id: string | null) => void
}

export const useAuditStore = create<AuditStore>((set) => ({
  // ... existing
  selectedId: null,
  setSelectedId: (id) => set({ selectedId: id }),
}))
```

---

## 14. Troubleshooting

### Problem: "WebSocket connection failed"

**Symptoms**: Red "Disconnected" status, console errors

**Solutions**:
1. Ensure Go proxy is running on port 8081
2. Check for CORS issues in browser console
3. Verify URL: `ws://localhost:8081/ws`

### Problem: "Results not appearing"

**Symptoms**: Connected, but no results show

**Solutions**:
1. Check browser console for JSON parse errors
2. Verify message format matches expected type
3. Add console.log in useWebSocket to debug

### Problem: "npm install fails"

**Solutions**:
```bash
# Clear npm cache
npm cache clean --force

# Remove node_modules and reinstall
rm -rf node_modules package-lock.json
npm install
```

### Problem: "TypeScript errors"

**Solutions**:
1. Run `npm run build` to see all errors
2. Ensure types match in `src/types/audit.ts`
3. Check for missing properties in objects

### Problem: "Tailwind classes not working"

**Solutions**:
1. Check `tailwind.config.js` content paths
2. Verify `@import "tailwindcss"` in index.css
3. Restart Vite dev server

---

## Summary

The React Dashboard:
1. ✅ Connects to Go Proxy via WebSocket (port 8081)
2. ✅ Receives real-time audit results
3. ✅ Displays results with color-coded trust scores
4. ✅ Shows claim-by-claim verification
5. ✅ Uses Zustand for state management
6. ✅ Styled with Tailwind CSS

**All components working** - the dashboard is production-ready.

---

*Back to [INDEX.md](INDEX.md) for the full documentation index.*
