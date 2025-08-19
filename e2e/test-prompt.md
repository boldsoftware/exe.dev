Please test exed. Doing so requires operating interactively with SSH, so read
tmux.md for information on how to do so.

When you are done, create a file called "result.txt" with either SUCCESS or FAILURE
in it, and commit. Please also create "reprot/report.md" with details about what you did
including "screenshots". If testing a web thing, that can be real screenshots. If testing
interactive terminal things, just include the text from the tmux output!

To speed things up in terms of discovering how things work, amend
this file with <hint>...</hint> blocks. For example, if you've discovered
how to build or start something, indicate that in <hint> blocks so future
test runs don't need to rediscover the whole thing.

Test Setup:

1. Verify that "docker ps" works before you start. (Docker should already
   be running; just verify that it's working.)

2. Build and start exed (in dev mode).

3. Build and start sshpiper.

4. Create yourself an ssh key with ssh-keygen.

Test Steps:

1. Register with exed by running "ssh -p 2222 localhost" 

The e-mail thing will require doing a web call; use the browser to handle that.

2. Test the "whoami" command.

3. Test the "list" command (should have no machines yet!)

4. Create a new container using "ssh -p 2222 localhost"

5. SSH into that container (by using ssh -p 2222 name@localhost) and run "id"
  (docker exec is not the right way to do this)

6. Open up a shell to the new container and check the hostname.

7. Use scp with the new container to test that SSH subsystems work.
