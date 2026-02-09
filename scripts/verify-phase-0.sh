#!/bin/bash

# Phase 0 Verification Script
# This script verifies that all Phase 0 setup is complete

set -e

echo "🔍 TruthTable Phase 0 Verification"
echo "===================================="
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

check_pass() {
    echo -e "${GREEN}✅ $1${NC}"
}

check_fail() {
    echo -e "${RED}❌ $1${NC}"
    exit 1
}

check_warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

# Check directory structure
echo "📁 Checking directory structure..."
for dir in backend-go backend-python frontend-react proto config docs; do
    if [ -d "$dir" ]; then
        check_pass "$dir exists"
    else
        check_fail "$dir is missing"
    fi
done
echo ""

# Check Go setup
echo "🔧 Checking Go setup..."
if [ -f "backend-go/go.mod" ]; then
    check_pass "Go module initialized"
else
    check_fail "Go module missing"
fi
echo ""

# Check Python setup
echo "🐍 Checking Python setup..."
if [ -f "backend-python/pyproject.toml" ]; then
    check_pass "Python project configured"
else
    check_fail "Python project configuration missing"
fi
echo ""

# Check Proto definitions
echo "📡 Checking Protocol Buffer definitions..."
if [ -f "proto/evaluator.proto" ]; then
    check_pass "Proto file exists"
else
    check_fail "Proto file missing"
fi
echo ""

# Check Docker Compose
echo "🐳 Checking Docker Compose..."
if [ -f "docker-compose.yml" ]; then
    check_pass "Docker Compose file exists"
    # Validate the file
    if docker-compose config > /dev/null 2>&1; then
        check_pass "Docker Compose config is valid"
    else
        check_warn "Docker Compose config validation failed (Docker might not be running)"
    fi
else
    check_fail "Docker Compose file missing"
fi
echo ""

# Check configuration files
echo "⚙️  Checking configuration files..."
if [ -f "config/prometheus.yml" ]; then
    check_pass "Prometheus config exists"
else
    check_fail "Prometheus config missing"
fi

if [ -f "config/grafana/datasources/prometheus.yml" ]; then
    check_pass "Grafana datasource config exists"
else
    check_fail "Grafana datasource config missing"
fi
echo ""

# Check documentation
echo "📚 Checking documentation..."
if [ -f "README.md" ]; then
    check_pass "README.md exists"
else
    check_fail "README.md missing"
fi

if [ -f "Makefile" ]; then
    check_pass "Makefile exists"
else
    check_fail "Makefile missing"
fi
echo ""

# Count Python __init__.py files
echo "🔍 Checking Python package structure..."
init_count=$(find backend-python/src -name "__init__.py" | wc -l)
if [ "$init_count" -gt 0 ]; then
    check_pass "Found $init_count __init__.py files"
else
    check_warn "No __init__.py files found in Python source"
fi
echo ""

# Summary
echo "=================================="
echo "✨ Phase 0 Verification Complete!"
echo "=================================="
echo ""
echo "Next steps:"
echo "  1. Run 'make install' to install dependencies"
echo "  2. Run 'make up' to start infrastructure"
echo "  3. Run 'make ollama-pull' to download the LLM model"
echo "  4. Begin Phase 1 implementation"
echo ""
