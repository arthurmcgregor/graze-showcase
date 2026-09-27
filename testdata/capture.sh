#!/usr/bin/env bash
# Capture GReader response fixtures from the live FreshRSS instance.
#
# The response shapes are the main thing that can be wrong, so the client is
# tested against real responses rather than hand-written ones. Re-run this
# after a FreshRSS upgrade; a diff in testdata/ is a diff in the contract.
#
# It logs in as graze itself does, with graze's own API password:
#
#   FRESHRSS_API_BASE=https://rss.example.com/api/greader.php \
#   FRESHRSS_API_USER=reader FRESHRSS_API_PASSWORD=... testdata/capture.sh
#
# Auth tokens are scrubbed on the way out. Nothing else is, so a capture holds
# your real subscriptions and articles; the committed fixtures were made
# synthetic afterwards, keeping every shape the tests assert on.

set -euo pipefail
cd "$(dirname "$0")"

BASE=${FRESHRSS_API_BASE:?set FRESHRSS_API_BASE, e.g. https://rss.example.com/api/greader.php}
USER=${FRESHRSS_API_USER:?set FRESHRSS_API_USER}
# A category whose name and feeds carry characters FreshRSS escapes.
CATEGORY=${FRESHRSS_CATEGORY:-Another & Ampersand}
# A feed FreshRSS fetches the full text for, with the append content action.
FULLTEXT_FEED=${FRESHRSS_FULLTEXT_FEED:-feed/97}
: "${FRESHRSS_API_PASSWORD:?set FRESHRSS_API_PASSWORD (see the header of this script)}"

# Password goes through the environment and stdin, never through argv.
# No trailing newline: PHP reads one as part of the last value, so a printed
# body logs in as a password with \n on the end.
form() { U="$1" P="$2" python3 -c '
import os, sys, urllib.parse
sys.stdout.write(urllib.parse.urlencode({"Email": os.environ["U"], "Passwd": os.environ["P"]}))'; }

pretty() { python3 -m json.tool --indent 2; }

scrub() { sed -e "s|${TOKEN}|<auth-token>|g" -e "s|${EDIT_TOKEN}|<edit-token>|g"; }

get() { curl -sS --max-time 30 -H "Authorization: GoogleLogin auth=${AUTH}" "$BASE$1"; }

echo "→ ClientLogin as ${USER}"
LOGIN=$(form "$USER" "$FRESHRSS_API_PASSWORD" |
  curl -sS --max-time 30 --data-binary @- -w '\nstatus=%{http_code}' "$BASE/accounts/ClientLogin")
AUTH=$(printf '%s' "$LOGIN" | sed -n 's/^Auth=//p' | tr -d '\r')
[ -n "$AUTH" ] || {
  echo "login failed as '${USER}':"
  printf '%s\n' "$LOGIN"
  echo
  echo "401 means the username and the API password do not belong to the same"
  echo "FreshRSS account, or that account has no API password set:"
  echo "  docker exec freshrss cli/update-user.php --user ${USER} --api-password '<new>'"
  exit 1
}
TOKEN=${AUTH#*/}

echo "→ edit token"
EDIT_TOKEN=$(get /reader/api/0/token)

printf 'SID=<sid>\nAuth=%s/<auth-token>\n' "$USER" > client_login.txt
printf '<edit-token>' > edit_token.txt

echo "→ subscription/list"
get '/reader/api/0/subscription/list?output=json' | pretty | scrub > subscription_list.json

echo "→ unread-count"
get '/reader/api/0/unread-count?output=json' | pretty | scrub > unread_count.json

echo "→ reading-list, unread only, first page"
get '/reader/api/0/stream/contents/user/-/state/com.google/reading-list?output=json&n=50&xt=user/-/state/com.google/read' \
  | pretty | scrub > stream_unread.json

echo "→ reading-list, n=2, to capture a continuation"
get '/reader/api/0/stream/contents/user/-/state/com.google/reading-list?output=json&n=2' \
  | pretty | scrub > stream_paged.json

echo "→ starred"
get '/reader/api/0/stream/contents/user/-/state/com.google/starred?output=json&n=20' \
  | pretty | scrub > stream_starred.json

echo "→ category ${CATEGORY}"
get "/reader/api/0/stream/contents/user/-/label/$(C="$CATEGORY" python3 -c '
import os, urllib.parse
print(urllib.parse.quote(os.environ["C"], safe=""))')?output=json&n=20" \
  | pretty | scrub > stream_category.json

echo "→ full text ${FULLTEXT_FEED}"
get "/reader/api/0/stream/contents/${FULLTEXT_FEED}?output=json&n=5" \
  | pretty | scrub > stream_fulltext.json

echo "→ rejected auth token"
curl -sS --max-time 30 -o unauthorized.txt -w 'status=%{http_code}\n' \
  -H 'Authorization: GoogleLogin auth=nobody/not-a-real-token' \
  "$BASE/reader/api/0/subscription/list?output=json" > unauthorized.status

echo
echo "captured:"
ls -1 ./*.json ./*.txt ./*.status
