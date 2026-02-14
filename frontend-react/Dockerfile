# ============================================
# TruthTable Dashboard - React
# ============================================
# Multi-stage build:
#   Stage 1: npm install + npm run build (creates static files)
#   Stage 2: Serve static files with nginx (fast, production-ready)
#
# WHY nginx? React builds into static HTML/CSS/JS files.
# Nginx serves these files efficiently and handles SPA routing
# (all routes serve index.html, React Router handles the rest).

# --- Stage 1: Build ---
FROM node:22-alpine AS builder

WORKDIR /app

# Copy dependency files first (Docker caching)
COPY package.json package-lock.json* ./
RUN npm ci

# Copy source and build
COPY . .
RUN npm run build

# --- Stage 2: Serve ---
FROM nginx:1.27-alpine

# Copy built assets to nginx's default serving directory
COPY --from=builder /app/dist /usr/share/nginx/html

# Copy nginx config (SPA routing)
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]
