#!/bin/bash
# Quick build script for hy2scale iKuai ipkg package
# Usage: cd ipkg && bash build-ipkg.sh
#
# Version is auto-read from internal/api/server.go (single source of truth)
# Output: hy2scale-<version>.ipkg in current directory

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Extract version from Go source (single source of truth)
VERSION=$(grep 'const Version' "$PROJECT_DIR/internal/api/server.go" | sed 's/.*"\(.*\)".*/\1/')
if [ -z "$VERSION" ]; then
  echo "ERROR: could not extract version from internal/api/server.go"
  exit 1
fi

echo "=== Building hy2scale v${VERSION} ipkg ==="

# Step 1: Update manifest.json version
echo "[1/5] Syncing version to manifest.json..."
sed -i "s/\"version\": *\"[^\"]*\"/\"version\": \"${VERSION}\"/" "$SCRIPT_DIR/hy2scale/manifest.json"

# Step 2: Build Docker image (DOCKER_BUILDKIT=0 for iKuai Docker 18.09 compat)
echo "[2/5] Building Docker image..."
cd "$PROJECT_DIR"
DOCKER_BUILDKIT=0 docker build -t hy2scale:ikuai .

# Step 3: Export image as offline package
echo "[3/5] Exporting Docker image..."
docker save hy2scale:ikuai | gzip > "$SCRIPT_DIR/hy2scale/docker_image.tar.gz"
IMAGE_SIZE=$(du -h "$SCRIPT_DIR/hy2scale/docker_image.tar.gz" | cut -f1)
echo "    Image size: ${IMAGE_SIZE}"

# Step 4: Convert SVG logo to PNG (if outdated or missing)
if [ "$PROJECT_DIR/hy2scale-v3.svg" -nt "$SCRIPT_DIR/hy2scale/ui/ico/app.png" ] || \
   [ ! -f "$SCRIPT_DIR/hy2scale/ui/ico/app.png" ]; then
  echo "[4/5] Converting logo SVG → PNG..."
  docker run --rm -v "$PROJECT_DIR:/work" python:3.12-alpine sh -c '
    apk add --no-cache -q cairo-dev py3-pip gcc musl-dev python3-dev >/dev/null 2>&1
    pip install -q cairosvg >/dev/null 2>&1
    python3 -c "
import cairosvg
cairosvg.svg2png(url=\"/work/hy2scale-v3.svg\", write_to=\"/work/ipkg/hy2scale/ui/ico/app.png\", output_width=256, output_height=256)
print(\"    Logo converted\")
"'
else
  echo "[4/5] Logo up to date, skipping"
fi

# Step 5: Pack ipkg
echo "[5/5] Packaging ipkg..."
cd "$SCRIPT_DIR"
tar -czf "hy2scale-${VERSION}.ipkg" hy2scale/
IPKG_SIZE=$(du -h "hy2scale-${VERSION}.ipkg" | cut -f1)

# Clean up embedded image (keep package small in git-ignored dir)
rm -f "$SCRIPT_DIR/hy2scale/docker_image.tar.gz"

echo ""
echo "=== Done ==="
echo "Output: ipkg/hy2scale-${VERSION}.ipkg (${IPKG_SIZE})"
echo ""
echo "Install on iKuai:"
echo "  Web: 高级应用 → 应用市场 → 本地安装"
