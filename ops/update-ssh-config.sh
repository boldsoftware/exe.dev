#!/bin/bash
# Shared function to update SSH config for exe-ctr-colima
# This ensures consistent SSH configuration across scripts

set -euo pipefail

COLIMA_PROFILE="${1:-exe-ctr-colima}"

update_exe_ssh_config() {
    local profile="$1"
    
    echo "Updating SSH configuration for $profile..."
    
    # Get the current SSH config from colima
    local ssh_config
    if ! ssh_config=$(colima ssh-config -p "$profile" 2>/dev/null); then
        echo "Error: Failed to get SSH config for $profile"
        return 1
    fi
    
    # Extract SSH details
    local ssh_port=$(echo "$ssh_config" | grep "Port" | awk '{print $2}')
    local ssh_host="127.0.0.1"
    local ssh_user="ubuntu"
    
    if [ -z "$ssh_port" ]; then
        echo "Error: Could not determine SSH port for $profile"
        return 1
    fi
    
    echo "  Found SSH port: $ssh_port"
    
    # Determine SSH key to use (prefer ed25519)
    local ssh_key_private
    if [ -f ~/.ssh/id_ed25519 ]; then
        ssh_key_private=~/.ssh/id_ed25519
    elif [ -f ~/.ssh/id_rsa ]; then
        ssh_key_private=~/.ssh/id_rsa
    else
        echo "Error: No SSH key found at ~/.ssh/id_ed25519 or ~/.ssh/id_rsa"
        return 1
    fi
    
    # Create the new SSH config entry
    local new_config="# Added by exe setup scripts
Host exe-ctr-colima
    HostName ${ssh_host}
    Port ${ssh_port}
    User ${ssh_user}
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    IdentityFile ${ssh_key_private}"
    
    # Update or add the SSH config entry
    if grep -q "^Host exe-ctr-colima$" ~/.ssh/config 2>/dev/null; then
        # Entry exists, update it
        echo "  Updating existing SSH config entry..."
        
        # Create a temp file with the updated config
        local temp_config=$(mktemp)
        local in_exe_block=false
        local skip_next=0
        
        while IFS= read -r line; do
            if [[ "$line" == "Host exe-ctr-colima" ]]; then
                in_exe_block=true
                skip_next=6  # Skip the host block (typically 6 lines after Host)
                echo "$new_config" >> "$temp_config"
            elif [ $skip_next -gt 0 ]; then
                # Skip lines that are part of exe-ctr-colima block
                if [[ "$line" =~ ^[[:space:]] ]] || [[ "$line" == "# Added by"* ]]; then
                    ((skip_next--))
                else
                    # Hit next host entry or non-indented line, stop skipping
                    skip_next=0
                    echo "$line" >> "$temp_config"
                fi
            else
                echo "$line" >> "$temp_config"
            fi
        done < ~/.ssh/config
        
        # Replace the original config
        mv "$temp_config" ~/.ssh/config
        chmod 600 ~/.ssh/config
    else
        # Entry doesn't exist, add it
        echo "  Adding new SSH config entry..."
        echo "" >> ~/.ssh/config
        echo "$new_config" >> ~/.ssh/config
    fi
    
    echo "✓ SSH config updated successfully"
    
    # Test the connection
    echo "Testing SSH connection..."
    if timeout 5 ssh -o ConnectTimeout=3 exe-ctr-colima "echo '✓ SSH connection successful'" 2>/dev/null; then
        echo "✓ SSH to ubuntu@exe-ctr-colima is working"
        return 0
    else
        echo "⚠️  Warning: SSH connection test failed"
        echo "  You may need to wait a moment for the VM to be ready"
        echo "  Or check that the ubuntu user is properly configured"
        return 1
    fi
}

# If sourced, don't run; if executed directly, run the update
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    update_exe_ssh_config "$COLIMA_PROFILE"
fi