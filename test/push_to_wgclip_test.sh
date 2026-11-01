#!/usr/bin/env bash
# Tests web/push-to-wgclip.sh against a fake warpgate tab, in bash and zsh (see test/README.md).
# The whole file must stay parseable by zsh: it re-runs itself as `zsh "$0" --suite`.

SELF=$0
SCRIPT=$(cd "$(dirname "$0")/.." && pwd)/web/push-to-wgclip.sh

# Plays the browser tab: verifies each hash file and consumes the push, or renames it to .failed.
fake_tab() {
    local ob=$1 mode=$2 log=$3 h c n want got
    while :; do
        for h in "$ob"/*.sha256; do
            [ -e "$h" ] || continue
            c=${h%.sha256}; n=${c##*/}
            if [ "$(cat "$mode")" = fail ]; then mv "$h" "$c.failed"; continue; fi
            want=$(head -n1 "$h"); got=$(sha256sum < "$c" | cut -d' ' -f1)
            if [ "$want" = "$got  $n" ]; then
                cp "$c" "$log/$n.content"; sed -n 2p "$h" > "$log/$n.title"; rm "$h" "$c"
            else
                echo "bad hash file $n" >> "$log/errors"; mv "$h" "$c.failed"
            fi
        done
        sleep 0.05
    done
}

pass=0 fail=0
check() {
    if [ "$2" = "$3" ]; then pass=$((pass + 1))
    else fail=$((fail + 1)); printf '  FAIL %s: got [%s] want [%s]\n' "$1" "$2" "$3"; fi
}
newest() { ls -t "$LOG"/*."$1" | head -n1; }

suite() {
    local W=$1 out err rc tab
    . "$SCRIPT"
    export WGCLIP_OUTBOX=$W/outbox
    LOG=$W/log
    mkdir -p "$WGCLIP_OUTBOX" "$LOG"
    echo ok > "$W/mode"
    bash "$SELF" --fake-tab "$WGCLIP_OUTBOX" "$W/mode" "$LOG" &
    tab=$!

    out=$(printf 'hällo wörld\nline2\n\n\n' | push-to-wgclip -t "my	title"); rc=$?
    check 'stdin: result' "$rc/$out" '0/pushed'
    check 'stdin: content, trailing newlines trimmed' "$(od -An -c "$(newest content)" | tr -s ' ')" \
        "$(printf 'hällo wörld\nline2' | od -An -c | tr -s ' ')"
    check 'title: control characters become spaces' "$(cat "$(newest title)")" 'my title'
    sleep 1.1
    printf 'file body\n' > "$W/notes.txt"
    out=$(push-to-wgclip -f "$W/notes.txt"); rc=$?
    check '-f: result' "$rc/$out" '0/pushed'
    check '-f: content unchanged' "$(od -An -c "$(newest content)" | tr -s ' ')" "$(od -An -c "$W/notes.txt" | tr -s ' ')"
    check '-f: title defaults to file name' "$(cat "$(newest title)")" 'notes.txt'
    push-to-wgclip -f "$W/missing" 2>/dev/null; check '-f: missing file' "$?" 1
    head -c 524289 /dev/zero | tr '\0' a > "$W/big"
    push-to-wgclip -f "$W/big" 2>/dev/null; check '-f: over limit' "$?" 1
    push-to-wgclip < "$W/big" 2>/dev/null; check 'stdin: over limit' "$?" 1
    head -c 524288 /dev/zero | tr '\0' b | push-to-wgclip > /dev/null; check 'stdin: exactly at limit' "$?" 0
    printf '\n\n' | push-to-wgclip 2>/dev/null; check 'stdin: only newlines' "$?" 1
    push-to-wgclip -T x < /dev/null 2>/dev/null; check '-T: not a number' "$?" 2
    push-to-wgclip -T 1a < /dev/null 2>/dev/null; check '-T: trailing garbage' "$?" 2

    echo fail > "$W/mode"
    err=$(echo doomed | push-to-wgclip 2>&1); rc=$?
    check 'failed push: result' "$rc/$err" '1/push-to-wgclip: push failed, see the warpgate tab'
    check 'failed push: files removed' "$(ls "$WGCLIP_OUTBOX" | wc -l | tr -d ' ')" 0

    kill "$tab"; wait "$tab" 2>/dev/null
    out=$(echo queued | push-to-wgclip -n); rc=$?
    check '-n: returns at once' "$rc/${out%% *}" '0/queued:'
    echo slow | push-to-wgclip -T 1 2>/dev/null; check 'no tab: times out' "$?" 1
    check 'no tab: both pushes stay queued (distinct names)' "$(ls "$WGCLIP_OUTBOX" | grep -c 'sha256$')" 2
    check 'no temp files left' "$(ls "$WGCLIP_OUTBOX" | grep -c tmp)" 0
    check 'fake tab saw no bad hash file' "$(cat "$LOG/errors" 2>/dev/null)" ''
    [ "$fail" -eq 0 ]
}

# Pasted into an interactive shell, the definition must survive history expansion.
paste_check() {
    local sh=$1 out
    out=$({ cat "$SCRIPT"; echo 'type push-to-wgclip > /dev/null 2>&1 && echo PASTE_$((40 + 2))_OK'; } |
        HOME=$TMP HISTFILE=/dev/null timeout 20 script -qec "$sh" /dev/null 2>&1)
    case $out in *PASTE_42_OK*) return 0 ;; esac
    printf '  FAIL paste into %s:\n%s\n' "$sh" "$(printf '%s\n' "$out" | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g' | tr -d '\r' | grep -a '^[a-z]*sh: ' | head -n3)"
    return 1
}

case $1 in
    --fake-tab) shift; fake_tab "$@"; exit ;;
    --suite) suite "$2"; rc=$?; echo "  $pass passed, $fail failed"; exit $rc ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
failed=0
for sh in bash zsh; do
    if ! command -v "$sh" > /dev/null; then echo "== $sh: not installed, skipped"; continue; fi
    echo "== $sh"
    "$sh" "$SELF" --suite "$TMP/$sh" || failed=1
    if command -v script > /dev/null; then
        flags='-f -i'; [ "$sh" = bash ] && flags='--norc -i'
        if paste_check "$sh $flags"; then echo '  paste into interactive shell: ok'; else failed=1; fi
    else
        echo '  paste check skipped: script(1) not installed'
    fi
done
exit $failed
