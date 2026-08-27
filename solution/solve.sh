#!/bin/bash
set -euo pipefail

cd /app
git apply --check /solution/fix.patch
git apply /solution/fix.patch
