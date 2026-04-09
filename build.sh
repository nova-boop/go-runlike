#!/bin/bash
# Save as build_all.sh

# 1. Preprocessing
go mod tidy

# 2. Define build targets
targets=(
    "android/arm64"
    "linux/amd64"
    "linux/arm64"
    "darwin/amd64"
    "darwin/arm64"
    "windows/amd64"
)

mkdir -p bin

for target in "${targets[@]}"; do
    IFS="/" read -r os arch <<< "$target"
    output="bin/runlike_${os}_${arch}"
    [ "$os" == "windows" ] && output+=".exe"

    echo "Building: $output ..."
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -ldflags="-s -w" -o "$output" main.go
done

echo "✅ Build complete! All files are located in the bin/ directory."

