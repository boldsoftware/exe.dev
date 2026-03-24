---
title: How do I copy files to/from my VM?
description: Transfer files with scp
subheading: "5. FAQ"
suborder: 4
published: true
---

Use `scp`.

```
scp file.txt vm.exe.xyz:~/
scp vm.exe.xyz:~/file.txt .
scp -r dir vm.exe.xyz:~/
```

## Piping through SSH

If `scp` isn't available, pipe through a plain SSH connection:

```
cat local-file | ssh vm.exe.xyz 'cat > ~/remote-file'
ssh vm.exe.xyz 'cat ~/remote-file' > local-file
tar cf - file dir | ssh vm.exe.xyz 'tar xf - -C ~/'
```
