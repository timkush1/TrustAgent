#!/bin/bash
# Generate Python code from proto files

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PROTO_DIR="$PROJECT_ROOT/../proto"
PY_OUT_DIR="$PROJECT_ROOT/src/truthtable/grpc/pb"

echo "Generating Python protobuf code..."
echo "Proto dir: $PROTO_DIR"
echo "Output dir: $PY_OUT_DIR"

# Ensure output directory exists
mkdir -p "${PY_OUT_DIR}"

# Generate Python code
python -m grpc_tools.protoc \
    --proto_path="${PROTO_DIR}" \
    --python_out="${PY_OUT_DIR}" \
    --grpc_python_out="${PY_OUT_DIR}" \
    "${PROTO_DIR}/evaluator.proto"

echo "✓ Python gRPC code generated successfully!"
echo ""
echo "Generated files:"
ls -lh "${PY_OUT_DIR}"/*.py 2>/dev/null || echo "  (files will appear after first run)"
