#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "==> Building UI Framework..."
cd ui-framework && npm ci && npm run build
cd ..

echo "==> Building App..."
cd app && npm ci && npm run build
cd ..

echo "==> Copying dist to internal/web/static/..."
rm -rf ../internal/web/static/*
cp -r app/dist/* ../internal/web/static/

echo "==> Done."
