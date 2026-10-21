#!/usr/bin/env bash
# Apply the GitHub repo settings for rossedman/tfmetrics: merge settings,
# topics, the rulesets in .github/rulesets/, security features and Pages.
# Run from the repo root with gh authenticated as a repo admin
# (gh auth refresh -s repo,workflow). Safe to re-run: rulesets are updated in
# place when they already exist.
set -euo pipefail

repo=rossedman/tfmetrics

gh repo edit "$repo" \
  --description "Org-wide Terraform metrics from static parsing: inventory, version drift, module adoption and hygiene, without terraform init, plan or state." \
  --add-topic terraform,opentofu,metrics,prometheus,opentelemetry,platform-engineering,golang,cli \
  --enable-issues --enable-wiki=false --enable-projects=false \
  --enable-squash-merge --enable-merge-commit=false --enable-rebase-merge=false \
  --enable-auto-merge --delete-branch-on-merge --allow-update-branch
gh api -X PATCH "repos/$repo" \
  -f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=PR_BODY >/dev/null

for file in .github/rulesets/*.json; do
  name=$(jq -r .name "$file")
  id=$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name==\"$name\") | .id")
  if [ -n "$id" ]; then
    gh api -X PUT "repos/$repo/rulesets/$id" --input "$file" >/dev/null
    echo "updated ruleset $name"
  else
    gh api -X POST "repos/$repo/rulesets" --input "$file" >/dev/null
    echo "created ruleset $name"
  fi
done

gh api -X PUT "repos/$repo/vulnerability-alerts"
gh api -X PUT "repos/$repo/automated-security-fixes"

# Pages publishes the docs site publicly even while the repo is private.
if [ "${PAGES:-no}" = yes ]; then
  gh api -X POST "repos/$repo/pages" -f build_type=workflow >/dev/null 2>&1 ||
    gh api -X PUT "repos/$repo/pages" -f build_type=workflow >/dev/null
  gh api -X PUT "repos/$repo/pages" -F https_enforced=true >/dev/null
  gh repo edit "$repo" --homepage https://rossedman.io/tfmetrics/
  echo "pages: $(gh api "repos/$repo/pages" --jq '{build_type, https_enforced, html_url}')"
fi

gh api "repos/$repo" --jq '{visibility, default_branch, allow_squash_merge, allow_merge_commit, delete_branch_on_merge}'
gh api "repos/$repo/rulesets" --jq '.[] | {name, enforcement}'
