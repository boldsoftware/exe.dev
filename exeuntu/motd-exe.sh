#!/bin/bash

# This is run by pam to set the motd for users who are logging in.

# Get the hostname
HOSTNAME=$(hostname)

# Check if welcomed process is running on port 8000 as exedev user
WELCOME_STATUS=""
if lsof -u exedev -i :8000 -sTCP:LISTEN 2>/dev/null | grep -q 'welcomed'; then
    WELCOME_STATUS="A web server is running at https://${HOSTNAME}/ (disable with 'systemctl disable --now welcome')."
fi

cat << EOF

You are on $(hostname -f). The disk is persistent. You have 'sudo'.
${WELCOME_STATUS}

For support and documentation, "ssh exe.dev" or visit https://exe.dev/

EOF