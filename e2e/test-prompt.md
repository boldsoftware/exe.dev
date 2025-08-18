Please test exed. Doing so requires operating interactively with SSH, so read
tmux.md for information on how to do so.

When you are done, create a file called "result.txt" with either SUCCESS or FAILURE
in it, and commit.

Test Setup:

1. Verify that "docker ps" works before you start.
2. Build and start exed (in dev mode).
3. Build and start sshpiper.
4. Create yourself an ssh key with ssh-keygen.

Test Steps:

1. Register with exed by running "ssh -p 2222 localhost" 

The e-mail thing will require doing a web call; use the browser to handle that.

2. Create a new container using "ssh -p 2222 localhost"

3. SSH into that container (by using ssh -p 2222 name@localhost) and run "id"
  (docker exec is not the right way to do this)
