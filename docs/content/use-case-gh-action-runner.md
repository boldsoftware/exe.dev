---
title: Running a self-hosted GitHub Actions Runner
description: log in easily into your CI environment
subheading: "3. Use Cases"
published: true
---

There's very little to it; you're following the GitHub instructions, but then
doing a little bit of systemd work to make sure the runner keeps running.

First, create a new box with `ssh exe.dev new`. This will create
a new box. SSH into it with `ssh box@exe.dev`. The trickiest
bit is to find the GitHub URLs. Replace the placeholders in the following:

 * [https://github.com/organizations/ORG/settings/actions/runners/new?arch=x64&os=linux](https://github.com/organizations/ORG/settings/actions/runners/new?arch=x64&os=linux)
 * [https://github.com/USER/REPO/settings/actions/runners/new?arch=x64&os=linux](https://github.com/USER/REPO/settings/actions/runners/new?arch=x64&os=linux)

Copy and paste the instructions from GitHub's instructions into your shell
session. It's pretty quick and easy until "run.sh".

To make sure the runner restarts after a reboot, we can create
a systemd service:

Create the service file at `/home/exedev/actions-runner/gh-actions-runner.service`:

```bash
cat > /home/exedev/actions-runner/gh-actions-runner.service << 'EOF'
[Unit]
Description=GitHub Actions Runner
After=network.target

[Service]
Type=simple
User=exedev
WorkingDirectory=/home/exedev/actions-runner
ExecStart=/home/exedev/actions-runner/run.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
```

Then copy the service file to systemd directory

```bash
sudo cp /home/exedev/actions-runner/gh-actions-runner.service /etc/systemd/system/
```

And start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gh-actions-runner.service
```

Verify the service is running

```bash
sudo systemctl status gh-actions-runner.service

gh-actions-runner.service - GitHub Actions Runner
     Loaded: loaded (/etc/systemd/system/gh-actions-runner.service; enabled; preset: enabled)
     Active: active (running) since Sun 2025-11-09 04:33:28 UTC; 41s ago
   Main PID: 1151 (run.sh)
      Tasks: 15 (limit: 2384)
     Memory: 93.2M (peak: 101.6M)
        CPU: 1.447s
     CGroup: /system.slice/gh-actions-runner.service
             /bin/bash /home/exedev/actions-runner/run.sh
             /bin/bash /home/exedev/actions-runner/run-helper.sh
             /home/exedev/actions-runner/bin/Runner.Listener run

Nov 09 04:33:30 ...exe.dev run.sh[1159]: Connected to GitHub
Nov 09 04:33:31 ...exe.dev run.sh[1159]: Current runner version: '2.329.0'
Nov 09 04:33:31 ...exe.dev run.sh[1159]: 2025-11-09 04:33:31Z: Listening for Jobs
```

You're all set!
