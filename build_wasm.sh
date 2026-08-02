#!/bin/bash
set -e

if [ -z "$WASI_SDK_PATH" ]; then
    echo "Error: WASI_SDK_PATH is not set."
    echo "Usage: WASI_SDK_PATH=/path/to/wasi-sdk ./build_wasm.sh"
    exit 1
fi

BUILD_DIR="build_wasm"
mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR"

TOOLCHAIN_FILE="$WASI_SDK_PATH/share/cmake/wasi-sdk.cmake"
if [ ! -f "$TOOLCHAIN_FILE" ]; then
    TOOLCHAIN_FILE="$WASI_SDK_PATH/share/cmake/wasi-sdk-p1.cmake"
fi

if [ ! -f "$TOOLCHAIN_FILE" ]; then
    echo "Error: Could not find WASI SDK toolchain file in $WASI_SDK_PATH/share/cmake/"
    exit 1
fi

cmake .. \
    -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
    -DCMAKE_BUILD_TYPE=Release

# Build/Compile
make -j$(nproc)

echo "Success! WASM binary is located at: $BUILD_DIR/colorer.wasm"