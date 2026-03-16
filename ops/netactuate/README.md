# NetActuate Anycast Deployment Playbook

Ansible playbook for provisioning NetActuate compute nodes with BGP anycast configuration using BIRD2.

## Quick Start with Claude Code

If you use Claude Code, you can set up this entire playbook by running the following prompt from within this directory. Replace the placeholder values with your actual settings provided by NetActuate:

```
Set up this NetActuate anycast deployment playbook for me. Here are my settings:

- API Key: <YOUR_API_KEY>
- Contract ID: 520
- BGP Group ID: 3277
- Origin ASN: 402146
- IPv4 Prefix: 161.210.92.0/22 (bind to <FIRST_USABLE_IP>)
- IPv6 Prefix: 2607:f740:e610::/48 (bind to <FIRST_USABLE_IP>)
- OS Image: Ubuntu 24.04 LTS (20240423)
- Node hostname pattern: edge1-POP.exe.dev
- Billing plan: VR8x4x50
- Locations: LAX,SJC,FRA,LHR,HKG,TYO,SIN,DFW,CHI,SYD,GRU,MIA,LGA,OTP,IAD3

Please:
1. Set up the Python venv and install ansible, naapi, and the netactuate ansible collection
2. Update group_vars/all with my settings above
3. Update keys.pub with my local SSH public key (use ~/.ssh/id_rsa.pub or ~/.ssh/id_ed25519.pub, whichever exists)
4. Update the hosts inventory with nodes for each location using my hostname pattern and plan
5. Run createnode.yaml to provision all nodes (re-run if any fail due to SSH timeouts)
6. Run bgp.yaml to configure BGP and BIRD on all nodes
7. Reboot all nodes to validate rc.local and BIRD start on boot
8. Show me how to verify BGP sessions and test anycast routing
```

## macOS Setup

### 1. Create a Python virtual environment and install dependencies

```bash
cd /path/to/netactuate-exe.dev-anycast
python3 -m venv .venv
source .venv/bin/activate
pip install --upgrade pip
pip install ansible
pip install git+https://github.com/netactuate/naapi.git@vapi2
```

### 2. Install the NetActuate Ansible collection

```bash
ansible-galaxy collection install git+https://github.com/netactuate/ansible-collection-compute.git,vapi2
```

### 3. Activate the venv (each new terminal session)

```bash
source .venv/bin/activate
```

## Configuration

Your API key, contract ID, and a quick reference for available locations/POPs, OS images, and plans can be found at:
https://portal.netactuate.com/account/api

Before using the API, you must whitelist your IP address. On the API page above, click **Manage API ACL(s)** and add the public IP of the machine you will be running Ansible from.

For full API documentation including the VAPI2/VAPI3 explorer, see:
https://www.netactuate.com/docs/

### group_vars/all

Contains account API settings and resource variables shared across all hosts. You must fill in:

- `auth_token` - your NetActuate API key (from the portal link above)
- `bgp_group` - your NetActuate BGP group ID
- `contract_id` - your billing contract ID (from the portal link above)
- `operating_system` - the OS image name (e.g. `Ubuntu 24.04 LTS (20240423)`)
- `bgp_networks` - your IPv4/IPv6 anycast prefixes with origin ASN

### keys.pub

SSH public key(s) that will be added to the `ubuntu` and `root` users on provisioned nodes.

### hosts

Ansible inventory file defining your infrastructure. Each node needs:

```
exe-POP-edge1.example.com location=POP bgp_enabled=True plan='VR8x4x50'
```

Recommended locations for a minimum global anycast deployment: LAX, SJC, FRA, LHR, HKG, TYO, SIN, DFW, CHI, SYD, GRU, MIA, LGA, OTP, IAD3. This can be expanded based on the other available POPs whenever necessary.

## Playbooks

### createnode.yaml

Provisions compute nodes and performs basic Ubuntu setup (apt upgrade, installs net-tools/sysstat/atop). Idempotent - will skip nodes that already exist.

```bash
ansible-playbook -i hosts createnode.yaml
```

When provisioning multiple nodes, some may take longer to boot and become reachable via SSH. Re-run `createnode.yaml` until there are no errors — it is idempotent and will skip nodes that are already provisioned and configured.

### bgp.yaml

Fetches BGP peering details from the NetActuate API, configures BIRD2 and binds anycast IPs to loopback via rc.local. Requests redundant BGP sessions (two peers per address family).

```bash
ansible-playbook -i hosts bgp.yaml
```

### deletenode.yaml

Destroys and deletes nodes listed in the hosts file via the NetActuate API.

```bash
ansible-playbook -i hosts deletenode.yaml
```

### Limit to a single node

```bash
ansible-playbook -i hosts createnode.yaml -l exe-LAX-edge1.example.com
```

### List available tags

```bash
ansible-playbook -i hosts bgp.yaml --list-tags
```

## Anycast IP Binding

The `bgp.yaml` playbook configures `rc.local` to bind the first IP from each anycast prefix to the loopback interface on every node. Based on the `ips` values configured in `bgp_networks` in `group_vars/all`, for example:

- **IPv4:** `<YOUR_IPV4_PREFIX>.1/32` bound to `lo`
- **IPv6:** `<YOUR_IPV6_PREFIX>::1/128` bound to `lo`

These are the anycast addresses announced via BGP. All nodes advertise the same prefixes, so traffic is routed to the nearest POP.

## Validation

### Reboot all nodes

After running `bgp.yaml`, reboot all nodes to confirm that `rc.local` and BIRD start correctly on boot:

```bash
ansible -i hosts nodes -m reboot -u ubuntu --become
```

### Verify BGP sessions

SSH to a node, sudo to root, and check BGP session state and announced prefixes:

```bash
ssh ubuntu@<node-ip>
sudo -i
birdc 'show protocols all' | grep -E "(Name|BGP|Neighbor address|Last error|Routes)"
```

### Test anycast routing

Use https://ping.pe to test reachability from multiple global locations:

- IPv4: `https://ping.pe/<YOUR_ANYCAST_IPV4>`
- IPv6: `https://ping.pe/<YOUR_ANYCAST_IPV6>`

Responses should come from different POPs depending on the source location, confirming anycast is working.
