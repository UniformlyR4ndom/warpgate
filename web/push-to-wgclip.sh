push-to-wgclip() {
    # Pushes text into the warpgate clipboard through the outbox folder watched by an open warpgate tab.
    # Outbox: $WGCLIP_OUTBOX, default ~/wgclip-outbox. Limit: 512 KiB. Works in bash and zsh.
    #   ls -la | push-to-wgclip -t 'dir listing'
    #   push-to-wgclip -f notes.txt
    local usage='usage: push-to-wgclip [-t title] [-f file] [-n] [-T timeout_sec] < text'
    local limit=524288 title='' src='' nowait='' timeout=30 opt OPTIND=1 OPTARG
    while getopts 't:f:nT:h' opt; do
        case $opt in
            t) title=$OPTARG ;;
            f) src=$OPTARG ;;
            n) nowait=1 ;;
            T) timeout=$OPTARG ;;
            *) echo "$usage" >&2; return 2 ;;
        esac
    done
    # No exclamation marks anywhere: interactive shells apply history expansion to pasted code.
    case $timeout in ''|*[^0-9]*) echo "push-to-wgclip: invalid timeout: $timeout" >&2; return 2 ;; esac

    local outbox=${WGCLIP_OUTBOX:-$HOME/wgclip-outbox}
    mkdir -p -- "$outbox" || return 1
    local name
    name="push-$(date +%Y%m%d-%H%M%S)-$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n').txt"
    local file=$outbox/$name size

    if [ -n "$src" ]; then
        [ -f "$src" ] || { echo "push-to-wgclip: not a file: $src" >&2; return 1; }
        size=$(wc -c < "$src") || return 1
        if [ "$size" -gt "$limit" ]; then
            echo "push-to-wgclip: too large (> 512 KiB); meant for text/small content" >&2
            return 1
        fi
        cp -- "$src" "$file" || return 1
        [ -n "$title" ] || title=${src##*/}
    else
        if [ -t 0 ]; then echo "$usage" >&2; return 2; fi
        head -c $((limit + 1)) > "$file" || { rm -f -- "$file"; return 1; }
        size=$(wc -c < "$file")
        if [ "$size" -gt "$limit" ]; then
            rm -f -- "$file"
            echo "push-to-wgclip: too large (> 512 KiB); meant for text/small content" >&2
            return 1
        fi
        local text
        text=$(cat -- "$file")
        if [ -z "$text" ]; then rm -f -- "$file"; echo 'push-to-wgclip: nothing to push' >&2; return 1; fi
        printf '%s' "$text" > "$file" || return 1
    fi

    local hash
    if command -v sha256sum > /dev/null; then
        hash=$(sha256sum < "$file")
    else
        hash=$(shasum -a 256 < "$file")
    fi || return 1
    hash=${hash%% *}
    # The hash file triggers the push, so it appears only once complete.
    {
        printf '%s  %s\n' "$hash" "$name"
        [ -z "$title" ] || { printf '%s' "$title" | tr '\000-\037\177' ' '; echo; }
    } > "$file.sha256.tmp" && mv -f -- "$file.sha256.tmp" "$file.sha256" || return 1

    if [ -n "$nowait" ]; then echo "queued: $name"; return 0; fi
    local i=0
    while [ "$i" -lt $((timeout * 5)) ]; do
        sleep 0.2
        if [ -e "$file.failed" ]; then
            # the browser may still be checking the renamed file
            sleep 1
            rm -f -- "$file.failed" "$file"
            echo 'push-to-wgclip: push failed, see the warpgate tab' >&2
            return 1
        fi
        [ -e "$file.sha256" ] || [ -e "$file" ] || { echo 'pushed'; return 0; }
        i=$((i + 1))
    done
    echo "push-to-wgclip: not picked up within $timeout s: no warpgate tab open, outbox not connected, or auth key not entered? The push stays queued in $outbox." >&2
    return 1
}
