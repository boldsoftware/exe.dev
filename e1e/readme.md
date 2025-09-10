These are end-to-end tests.

They're called e1e because e2e was already taken when they were created.

They spin up the full stack (exed, piperd) and a few external services (a fake email server) and then interact with the system the way a user would.

For debugging, run with -v and look at the instructions it prints at the top.

The e1e tests generate two forms of recordings:

- *.cast files, which are asciinema recordings of what the user sees. You can play them locally with `asciinema play TestFoo.cast`.
  In CI, these are bundled into a standalone HTML viewer that is made available as an artifact.

- golden/*.txt files, see e1e/golden/readme.md for documentation of them.
