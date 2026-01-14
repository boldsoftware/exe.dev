
# Print exe.dev message (only in interactive shells)
if [[ $- == *i* ]]; then
    echo ""
    echo "You are on $(hostname -f). The disk is persistent. You have 'sudo'."
    echo ""
    echo 'For support and documentation, "ssh exe.dev" or visit https://exe.dev/'
    echo ""

    # Build shelley/xterm URLs based on hostname
    _exe_url() {
        local fqdn prefix suffix
        fqdn=$(hostname -f)
        if [[ "$fqdn" == *.* ]]; then
            prefix=${fqdn%%.*}
            suffix=${fqdn#*.}
            echo "https://${prefix}.${1}.${suffix}/"
        else
            echo "https://${fqdn}.${1}.exe.xyz/"
        fi
    }

    hints=(
	  $'Read exe.dev docs at https://exe.dev/docs'
	  "$(printf 'Shelley, our coding agent, is running at %s' "$(_exe_url shelley)")"
	  $'Docker is installed and works; try "docker run --rm alpine:latest echo hello world"'
	  "$(printf 'If you run an http webserver on port 4444, you can access it securely at https://%s:4444\nTry it with "python3 -m http.server 4444"' "$(hostname -f)")"
	  $'ssh into exe.dev to manage the HTTP proxy and sharing for this VM'
	  "$(printf 'There is a web-based terminal at %s' "$(_exe_url xterm)")"
    )
    unset -f _exe_url

    hint_index=$((RANDOM % ${#hints[@]}))
    printf '%s\n' "${hints[hint_index]}"

    echo ""
fi
