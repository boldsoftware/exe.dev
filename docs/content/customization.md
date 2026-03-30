---
title: Customizing VMs
description: Three ways to customize your exe.dev VMs
subheading: "2. Features"
suborder: 3
---

Customize your exe.dev VMs!

## Just Use SSH

The simplest and most common approach is to create a VM,
and then use `ssh`, `scp`, `rsync`, etc. to customize
your VM. Some users clone a repo (possibly using the GitHub
integration) and others have a script they run.

See [How do I copy files to/from my VM?](/docs/faq/copy-files) for more.

## Use a custom Docker image

You can customize a Docker image, publish it, and create new
VMs using it. The [Dockerfile for exeuntu](https://github.com/boldsoftware/exeuntu/blob/main/Dockerfile)
is open source, so you can use that as a base if you'd like.

```
ssh exe.dev new --image=myorg/my-custom-image:latest
```

## Setup scripts

The exeuntu image runs `/exe.dev/setup` at first boot, once. This
file can be specified with `new --setup-script` as well
as `cat script | ssh exe.dev defaults write dev.exe new.setup-script`.

It is easiest to create a script and pipe it into the `new` command:

```
$ cat setup.sh
#!/bin/sh
touch /tmp/foo
$ cat setup.sh | ssh exe.dev new --setup-script /dev/stdin
...
$ ssh scarlet-nebula.exe.xyz ls -l /tmp/foo
-rw-r--r-- 1 exedev exedev 0 Mar 28 00:49 /tmp/foo
```

You can do it inline as well:

```
$ ssh exe.dev new --name my-vm --setup-script '"touch /tmp/no-shebang"'
...
$ ssh my-vm.exe.xyz ls -l /tmp/no-shebang
-rw-r--r-- 1 exedev exedev 0 Mar 28 00:52 /tmp/no-shebang
```

Or, multi-line:

```
exe.dev ▶ new --name lynx-zebra --setup-script "#!/bin/python3\nopen('/tmp/foo', 'w')"
...
exe.dev ▶ ssh lynx-zebra ls -l /tmp/foo
-rw-r--r-- 1 exedev exedev 0 Mar 28 00:50 /tmp/foo
```

If you want a default for all your VMs:

```
$ (echo '#!/bin/bash'; echo touch /tmp/fine) | ssh exe.dev defaults write dev.exe new.setup-script
```

To clear the default:

```
$ ssh exe.dev defaults delete dev.exe new.setup-script
```

Setup scripts have a maximum size. Use indirection.
