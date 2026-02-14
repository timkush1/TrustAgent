# TrustAgent React Dashboard

Real-time monitoring dashboard for the TrustAgent hallucination detection system.

## Features

- Live audit feed via WebSocket
- Trust score gauges with color-coded grades (A-F)
- Claim-by-claim verification breakdown
- Hallucination detection badges
- Dark theme

## Quick Start

```bash
npm install
npm run dev
# Open http://localhost:5173
```

## Tech Stack

- React 19 + TypeScript
- Vite (build tool)
- Tailwind CSS (styling)
- Zustand (state management)
- WebSocket (real-time updates from Go proxy)

## Project Structure

```
src/
├── main.tsx              # Entry point
├── App.tsx               # Root component
├── components/
│   ├── audit/            # AuditCard, ClaimList, TrustScoreGauge
│   ├── dashboard/        # Dashboard layout
│   └── layout/           # Header
├── stores/
│   └── auditStore.ts     # Zustand store
├── hooks/
│   └── useWebSocket.ts   # WebSocket connection hook
└── types/
    └── audit.ts          # TypeScript definitions
```

## Environment

The dashboard connects to the Go proxy for WebSocket updates:

| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_API_URL` | `http://localhost:8080` | Go proxy HTTP URL |
| `VITE_WS_URL` | `ws://localhost:8080/ws` | Go proxy WebSocket URL |
