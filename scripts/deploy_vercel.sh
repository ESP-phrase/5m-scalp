#!/usr/bin/env bash
# scripts/deploy_vercel.sh
# Usage:
#   export VERCEL_TOKEN=your_token
#   cd repo_root
#   ./scripts/deploy_vercel.sh [.env]

set -euo pipefail
ENVFILE=${1:-.env}
if [ ! -f "$ENVFILE" ]; then
  echo "env file not found: $ENVFILE" >&2
  exit 1
fi
if [ -z "${VERCEL_TOKEN:-}" ]; then
  echo "Set VERCEL_TOKEN (create one at https://vercel.com/account/tokens)" >&2
  exit 1
fi
# Link project (interactive if not linked)
if ! vercel --version >/dev/null 2>&1; then
  echo "Please install vercel CLI: npm i -g vercel" >&2
  exit 1
fi
echo "Linking project (if not linked, follow prompts)..."
vercel link --token "$VERCEL_TOKEN" || true

echo "Adding env vars from $ENVFILE to Vercel (production target)."
while IFS= read -r line; do
  # skip empty and comments
  [[ -z "$line" ]] && continue
  [[ "$line" =~ ^# ]] && continue
  if [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
    key=${BASH_REMATCH[1]}
    val=${BASH_REMATCH[2]}
    # remove surrounding quotes
    val=${val#"}
    val=${val%"}
    val=${val#'}
    val=${val%'}
    echo "Adding $key..."
    # Pipe the value to interactive prompt of vercel env add
    printf '%s' "$val" | vercel env add "$key" production --token "$VERCEL_TOKEN" || {
      echo "Failed to add $key via CLI, you may need to add it manually." >&2
    }
  fi
done < "$ENVFILE"

echo "All done. Deploy with: vercel --prod --token $VERCEL_TOKEN"
