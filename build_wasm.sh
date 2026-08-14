#!/bin/bash

#
# Default paths:
#   BINARYEN_PATH=/opt/binaryen
#   WASI_SDK_PATH=/opt/wasi-sdk
#
# Override them if necessary:
#   BINARYEN_PATH=/path/to/binaryen WASI_SDK_PATH=/path/to/wasi-sdk ./build_wasm.sh
#

set -e

BINARYEN_PATH="${BINARYEN_PATH:-/opt/binaryen}"
WASI_SDK_PATH="${WASI_SDK_PATH:-/opt/wasi-sdk}"

echo "Using Binaryen: $BINARYEN_PATH"
echo "Using WASI SDK: $WASI_SDK_PATH"

# ----------------------------------------------------------------------
# Check Binaryen
# ----------------------------------------------------------------------

WASM_OPT="$BINARYEN_PATH/bin/wasm-opt"

if [ ! -x "$WASM_OPT" ]; then
    echo
    echo "Error: Binaryen was not found at:"
    echo "  $BINARYEN_PATH"
    echo
    echo "Please set BINARYEN_PATH to your Binaryen installation."
    echo "For example:"
    echo "  BINARYEN_PATH=/path/to/binaryen ./build_wasm.sh"
    exit 1
fi

# ----------------------------------------------------------------------
# Check WASI SDK
# ----------------------------------------------------------------------

TOOLCHAIN_FILE="$WASI_SDK_PATH/share/cmake/wasi-sdk.cmake"

if [ ! -f "$TOOLCHAIN_FILE" ]; then
    TOOLCHAIN_FILE="$WASI_SDK_PATH/share/cmake/wasi-sdk-p1.cmake"
fi

if [ ! -f "$TOOLCHAIN_FILE" ]; then
    echo
    echo "Error: WASI SDK was not found at:"
    echo "  $WASI_SDK_PATH"
    echo
    echo "Please set WASI_SDK_PATH to your WASI SDK installation."
    echo "For example:"
    echo "  WASI_SDK_PATH=/path/to/wasi-sdk ./build_wasm.sh"
    exit 1
fi

# ----------------------------------------------------------------------
# Configure and build
# ----------------------------------------------------------------------

BUILD_DIR="build_wasm"

mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR"

cmake .. \
    -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
    -DCMAKE_BUILD_TYPE=Release

cmake --build . --parallel "$(nproc)"

# ----------------------------------------------------------------------
# Optimize WASM
# ----------------------------------------------------------------------

WASM_FILE="colorer.wasm"
OPTIMIZED_FILE="colorer.optimized.wasm"

echo
echo "Optimizing WASM with Binaryen..."

"$WASM_OPT" \
    -O3 \
    "$WASM_FILE" \
    -o "$OPTIMIZED_FILE"

mv "$OPTIMIZED_FILE" "$WASM_FILE"

echo
echo "Success!"
echo "WASM binary: $BUILD_DIR/$WASM_FILE"
