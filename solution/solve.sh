#!/bin/bash
set -euo pipefail

cd /app
git apply --check /solution/fix.patch
git apply /solution/fix.patch
git apply --check /solution/auth-realm.patch
git apply /solution/auth-realm.patch
git apply --check /solution/auth-redirect.patch
git apply /solution/auth-redirect.patch
git apply --check /solution/registry-filesystem-boundaries.patch
git apply /solution/registry-filesystem-boundaries.patch
