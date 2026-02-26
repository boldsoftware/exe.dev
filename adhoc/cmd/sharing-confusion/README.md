# sharing-confusion

Generates per-owner notification emails about the port-sharing exposure bug,
where shared VMs gave access to ports beyond what was explicitly shared.

## Usage

1. Generate the JSON email files from the CSV:

```
go run ./adhoc/cmd/sharing-confusion/
```

This reads `adhoc/cross_user_access.csv` and writes one JSON file per VM owner
into `adhoc/sharing-confusion-emails/`.

2. Preview and send the emails using the emailtool:

```
go run ./adhoc/cmd/emailtool/ -dir adhoc/sharing-confusion-emails
```

Then open http://localhost:8033 to preview emails and send them individually.

Sending requires `POSTMARK_API_KEY` to be set in the environment.

## Flags

**sharing-confusion:**
- `-csv` — path to CSV (default: `adhoc/cross_user_access.csv`)
- `-out` — output directory (default: `adhoc/sharing-confusion-emails`)
- `-template` — path to HTML template (default: `adhoc/cmd/sharing-confusion/template.html`)

**emailtool:**
- `-dir` — directory of JSON email files (required)
- `-addr` — listen address (default: `:8033`)

## Re-running

Re-running sharing-confusion regenerates the email content but preserves the
`sent` status of any emails that were already sent.
