#!/bin/sh
# signpath-sign.sh <artifact> — Authenticode-sign a Windows exe via SignPath.
#
# DORMANT BY DESIGN: exits 0 immediately when SIGNPATH_API_TOKEN is unset, so
# the release pipeline is unchanged until the SignPath Foundation OSS
# application is approved and the CI secrets are added. See RELEASING.md.
#
# Required env when active:
#   SIGNPATH_API_TOKEN     CI user API token (CI secret)
#   SIGNPATH_ORG_ID        organization id (uuid)
# Optional env:
#   SIGNPATH_PROJECT       project slug   (default: auxly-cli)
#   SIGNPATH_POLICY        signing policy (default: release-signing)
set -eu

ARTIFACT="${1:?usage: signpath-sign.sh <artifact>}"

# Dormant: no token, no-op. Non-Windows artifacts pass through untouched too.
[ -n "${SIGNPATH_API_TOKEN:-}" ] || exit 0
case "$ARTIFACT" in
  *.exe) ;;
  *) exit 0 ;;
esac

ORG="${SIGNPATH_ORG_ID:?SIGNPATH_ORG_ID must be set when SIGNPATH_API_TOKEN is}"
PROJECT="${SIGNPATH_PROJECT:-auxly-cli}"
POLICY="${SIGNPATH_POLICY:-release-signing}"
API="https://app.signpath.io/API/v1/$ORG"
AUTH="Authorization: Bearer $SIGNPATH_API_TOKEN"

echo "signpath: submitting $(basename "$ARTIFACT") to $PROJECT/$POLICY" >&2
LOCATION=$(curl -sSf -D - -o /dev/null \
  -H "$AUTH" \
  -F "ProjectSlug=$PROJECT" \
  -F "SigningPolicySlug=$POLICY" \
  -F "Artifact=@$ARTIFACT" \
  "$API/SigningRequests" | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
[ -n "$LOCATION" ] || { echo "signpath: no signing-request location returned" >&2; exit 1; }

# Poll until the request completes (releases are rare; be patient but bounded).
i=0
while [ $i -lt 60 ]; do
  sleep 10
  STATUS=$(curl -sSf -H "$AUTH" "$LOCATION" | tr -d ' \n' | sed -n 's/.*"status":"\([A-Za-z]*\)".*/\1/p')
  case "$STATUS" in
    Completed) break ;;
    Failed|Denied|Canceled) echo "signpath: signing request $STATUS" >&2; exit 1 ;;
  esac
  i=$((i + 1))
done
[ "$STATUS" = "Completed" ] || { echo "signpath: timed out waiting for signature" >&2; exit 1; }

# Replace the unsigned exe in place with the signed artifact.
curl -sSf -H "$AUTH" -o "$ARTIFACT.signed" "$LOCATION/SignedArtifact"
mv "$ARTIFACT.signed" "$ARTIFACT"
echo "signpath: signed $(basename "$ARTIFACT")" >&2
