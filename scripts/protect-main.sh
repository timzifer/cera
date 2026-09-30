#!/usr/bin/env sh
# Applies .github/rulesets/main.json to the repository: no direct pushes,
# force pushes or deletion of main; changes land through pull requests with
# a green "ci-ok" check. Needs the GitHub CLI logged in as a repository admin.
#
#   scripts/protect-main.sh [owner/repo]
#
# Alternatively import the JSON in Settings → Rules → Rulesets → New ruleset
# → Import a ruleset.
set -eu
repo=${1:-timzifer/cera}
dir=$(dirname "$0")/..
id=$(gh api "repos/$repo/rulesets" --jq '.[] | select(.name == "Protect main") | .id')
if [ -n "$id" ]; then
	gh api -X PUT "repos/$repo/rulesets/$id" --input "$dir/.github/rulesets/main.json" >/dev/null
	echo "updated ruleset $id"
else
	gh api -X POST "repos/$repo/rulesets" --input "$dir/.github/rulesets/main.json" --jq '"created ruleset \(.id)"'
fi
