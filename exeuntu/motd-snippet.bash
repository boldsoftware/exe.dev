
# Print exe.dev message (only in interactive shells)
if [[ $- == *i* ]]; then
    echo ""
    echo "You are on $(hostname -f). The disk is persistent. You have 'sudo'."
    echo ""
    echo 'For support and documentation, "ssh exe.dev" or visit https://exe.dev/'
    echo ""

    hints=(
	  $'Read exe.dev docs at https://exe.dev/docs'
	  "$(printf 'Shelley, our coding agent, is running at https://%s:9999' "$(hostname)")"
	  $'Docker is installed and works; try "docker --rm run alpine:latest echo hello world"'
	  "$(printf 'If you run an http webserver on port 1234, you can access it securely at https://%s:1234\nTry it with "python3 -m http.server 1234"' "$(hostname)")"
	  $'ssh into exe.dev to manage the HTTP proxy and sharing for this VM'
	  "$(printf 'There is a web-based terminal at https://%s.xterm.exe.dev/' "$(hostname | cut -d. -f1)")"
    )

    hint_index=$((RANDOM % ${#hints[@]}))
    printf '%s\n' "${hints[hint_index]}"

    echo ""
fi
