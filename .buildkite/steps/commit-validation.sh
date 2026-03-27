#!/usr/bin/env bash
# commit-validation.sh — Validate commits in origin/main..HEAD for the Buildkite commit queue.
# Mirrors the "Validate commits" logic from .github/workflows/queue-main.yml.
set -euo pipefail

has_errors=0

# ---------------------------------------------------------------------------
# 0. Fetch origin/main to get the latest gate files and merge base.
# ---------------------------------------------------------------------------
echo "--- Fetching origin/main"
git fetch origin main

# ---------------------------------------------------------------------------
# 1. Gate files from origin/main
# ---------------------------------------------------------------------------

# 1a. Required ancestor
REQUIRED_ANCESTOR=$(git show origin/main:.github/queue-gate-ancestor | grep -v '^#' | grep -v '^$' | head -1 || true)
REQUIRED_ANCESTOR=$(printf '%s' "$REQUIRED_ANCESTOR" | tr -d '[:space:]')

if [ -n "$REQUIRED_ANCESTOR" ]; then
    if ! git merge-base --is-ancestor "$REQUIRED_ANCESTOR" HEAD; then
        echo "❌ ERROR: Your branch does not include commit $REQUIRED_ANCESTOR."
        echo ""
        echo "This commit is required because it fixes a previously-landed bad commit."
        echo "Please fetch origin--be prepared for someone else having force-pushed!--and rebase"
        echo "your work on it."
        echo ""
        echo "********** Please BE CAREFUL NOT TO REINTRODUCE THE BAD COMMIT! **********"
        echo ""
        echo "See .github/queue-gate-ancestor on main for context."
        echo ""
        echo "Then push to the commit queue again."
        has_errors=1
    else
        echo "✅ Branch includes required ancestor ($REQUIRED_ANCESTOR)"
    fi
fi

# 1b. Known-bad subjects
mapfile -t KNOWN_BAD_SUBJECTS < <(git show origin/main:.github/queue-gate-bad-subjects | grep -v '^#' | grep -v '^$')

# ---------------------------------------------------------------------------
# 2. Per-commit checks
# ---------------------------------------------------------------------------
base_ref="origin/main"
end_ref="HEAD"

echo "--- Validating commits in range ${base_ref}..${end_ref}"

commits=$(git rev-list --reverse "${base_ref}".."${end_ref}" || true)

if [ -z "$commits" ]; then
    echo "No commits to validate"
    exit 0
fi

for commit in $commits; do
    echo "Checking commit $(git log --oneline -n 1 "$commit")"

    subject=$(git log --format="%s" -n 1 "$commit")
    full_message=$(git log --format="%B" -n 1 "$commit")

    # --- Known-bad subjects ---
    trimmed_subject=$(printf '%s' "$subject" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    for bad in "${KNOWN_BAD_SUBJECTS[@]+"${KNOWN_BAD_SUBJECTS[@]}"}"; do
        [ -z "$bad" ] && continue
        if [ "$trimmed_subject" = "$bad" ]; then
            echo "❌ ERROR: Commit $commit has a known-bad subject (previously reverted): $subject"
            has_errors=1
        fi
    done

    # --- Author name ---
    author_name=$(git log --format="%an" -n 1 "$commit")
    author_email=$(git log --format="%ae" -n 1 "$commit")
    case "$author_name" in
    "exe.dev user" | "exe.dev system" | ubuntu | shelley)
        echo "❌ ERROR: Commit $commit is authored by '$author_name' (a default/system user)"
        echo "Please set your git author name: git config user.name 'Your Name'"
        has_errors=1
        ;;
    esac

    # --- Author email: @*.exe.xyz but NOT @bored.exe.xyz ---
    if echo "$author_email" | grep -qE '@.*\.exe\.xyz$' && ! echo "$author_email" | grep -qE '@bored\.exe\.xyz$'; then
        echo "❌ ERROR: Commit $commit has an internal machine email: $author_email"
        echo "Please set your git author email: git config user.email 'you@example.com'"
        has_errors=1
    fi

    # --- WIP in subject (case insensitive) ---
    if echo "$subject" | grep -qi "WIP"; then
        echo "❌ ERROR: Commit $commit has 'WIP' in subject line: $subject"
        has_errors=1
    fi

    # --- VIBES in subject (case insensitive) ---
    if echo "$subject" | grep -qi "VIBES"; then
        echo "❌ ERROR: Commit $commit has 'VIBES' in subject line: $subject"
        has_errors=1
    fi

    # --- DO NOT SUBMIT in full message (case insensitive) ---
    if echo "$full_message" | grep -qi "DO NOT SUBMIT"; then
        echo "❌ ERROR: Commit $commit contains 'DO NOT SUBMIT' in commit message"
        echo "Full message:"
        echo "$full_message"
        echo "---"
        has_errors=1
    fi

    # --- Message composed only of dots ---
    trimmed_message=$(printf '%s' "$full_message" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    if [ -n "$trimmed_message" ] && echo "$trimmed_message" | grep -qE '^[.]+$'; then
        echo "❌ ERROR: Commit $commit has a message composed only of dots"
        has_errors=1
    fi

    # --- Merge commit ---
    parent_count=$(git rev-list --parents -n 1 "$commit" | awk '{print NF-1}')
    if [ "$parent_count" -gt 1 ]; then
        echo "❌ ERROR: Commit $commit is a merge commit: $subject"
        echo "We use a rebase workflow."
        has_errors=1
    fi

    # --- ui/dist/ artifacts (excluding .gitkeep) ---
    dist_files=$(git diff-tree --no-commit-id --diff-filter=d --name-only -r "$commit" | grep '^ui/dist/' | grep -v '^ui/dist/\.gitkeep$' || true)
    if [ -n "$dist_files" ]; then
        echo "❌ ERROR: Commit $commit contains ui/dist build artifacts: $subject"
        echo "Files:"
        echo "$dist_files"
        echo ""
        echo "ui/dist/ is generated by the build process and should not be committed."
        has_errors=1
    fi

    # --- Binary files without opt-in ---
    binary_files=$(git diff-tree --no-commit-id --diff-filter=d --numstat -r "$commit" | awk '$1 == "-" && $2 == "-" { print $3 }')
    if [ -n "$binary_files" ]; then
        if ! echo "$full_message" | grep -q "intentionally committing binary files"; then
            echo "❌ ERROR: Commit $commit contains binary files: $subject"
            echo "Binary files:"
            echo "$binary_files"
            echo ""
            echo "If this is intentional, include \"intentionally committing binary files\" in the commit message."
            has_errors=1
        fi
    fi
done

# ---------------------------------------------------------------------------
# 3. Cross-domain check
# ---------------------------------------------------------------------------
for commit in $commits; do
    files=$(git diff-tree --no-commit-id --name-only -r "$commit")
    if [ -z "$files" ]; then
        continue
    fi

    has_exeuntu=false
    has_shelley=false
    has_oss=false
    has_other=false

    while IFS= read -r file; do
        case "$file" in
        exeuntu/*) has_exeuntu=true ;;
        shelley/*) has_shelley=true ;;
        .github/workflows/shelley-tests.yml) has_shelley=true ;;
        oss/*) has_oss=true ;;
        *) has_other=true ;;
        esac
    done <<<"$files"

    count=0
    [ "$has_exeuntu" = true ] && count=$((count + 1))
    [ "$has_shelley" = true ] && count=$((count + 1))
    [ "$has_oss" = true ] && count=$((count + 1))
    [ "$has_other" = true ] && count=$((count + 1))

    if [ "$count" -gt 1 ]; then
        echo "❌ ERROR: Commit $(git log --oneline -n 1 "$commit") mixes files across domains."
        echo "Each commit must touch only exeuntu/, only shelley/, only oss/, or only other files."
        echo "Files in this commit:"
        echo "$files"
        has_errors=1
    fi
done

# ---------------------------------------------------------------------------
# Final verdict
# ---------------------------------------------------------------------------
if [ "$has_errors" -eq 1 ]; then
    echo ""
    echo "❌ Validation failed! Please fix the above issues and try again."
    exit 1
fi

echo "✅ All commits passed validation"
exit 0
