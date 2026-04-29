// Package drip implements drip email campaigns for trial onboarding.
//
// The Runner checks every hour for trial users who need emails and sends
// them according to the campaign schedule. Every decision (send or skip)
// is recorded in the drip_sends table for analysis.
package drip

import (
	"context"
	"log/slog"
	"time"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/stage"
)

const (
	campaignTrialOnboarding = "trial_onboarding"

	stepDay0Welcome  = "day0_welcome"
	stepDay1Nudge    = "day1_nudge"
	stepDay3Feature  = "day3_feature"
	stepDay5Urgency  = "day5_urgency"
	stepDay7Expiry   = "day7_expiry"
	stepDay10WinBack = "day10_winback"
	stepDay14Final   = "day14_final"

	statSent    = "sent"
	statSkipped = "skipped"
	statFailed  = "failed"
)

// step defines when and how a drip email is evaluated.
type step struct {
	name  string
	delay time.Duration // time after signup to send
}

// steps in chronological order.
var steps = []step{
	{stepDay0Welcome, 1 * time.Hour},
	{stepDay1Nudge, 24 * time.Hour},
	{stepDay3Feature, 72 * time.Hour},
	{stepDay5Urgency, 120 * time.Hour},
	{stepDay7Expiry, 168 * time.Hour},
	{stepDay10WinBack, 240 * time.Hour},
	{stepDay14Final, 336 * time.Hour},
}

// SendFunc sends an email. Matches the signature needed by the runner.
// The caller is responsible for logging the send.
type SendFunc func(ctx context.Context, msg email.Message) error

// Runner evaluates and sends drip campaign emails.
type Runner struct {
	db   *sqlite.DB
	env  stage.Env
	send SendFunc
	log  *slog.Logger
	now  func() time.Time // defaults to time.Now; override in tests
}

// NewRunner creates a new drip campaign runner.
func NewRunner(db *sqlite.DB, env stage.Env, send SendFunc, log *slog.Logger) *Runner {
	return &Runner{db: db, env: env, send: send, log: log, now: time.Now}
}

// Start begins the hourly drip check loop. It blocks until ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	// Run once immediately on startup.
	r.runOnce(ctx)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

// runOnce evaluates all trial users and sends any due emails.
func (r *Runner) runOnce(ctx context.Context) {
	cutoff := r.now().Add(-21 * 24 * time.Hour)
	users, err := exedb.WithRxRes1(r.db, ctx, (*exedb.Queries).ListTrialUsersForDrip, cutoff)
	if err != nil {
		r.log.ErrorContext(ctx, "drip: failed to list trial users", "error", err)
		return
	}

	for _, u := range users {
		if ctx.Err() != nil {
			return
		}
		r.processUser(ctx, u)
	}
}

// processUser evaluates one user against all campaign steps.
func (r *Runner) processUser(ctx context.Context, u exedb.ListTrialUsersForDripRow) {
	signupTime := u.TrialStartedAt
	now := r.now()

	// Load previous sends for this user+campaign.
	prevSends, err := exedb.WithRxRes1(r.db, ctx, (*exedb.Queries).GetDripSendsForUser, exedb.GetDripSendsForUserParams{
		UserID:   u.UserID,
		Campaign: campaignTrialOnboarding,
	})
	if err != nil {
		r.log.ErrorContext(ctx, "drip: failed to get sends for user", "user_id", u.UserID, "error", err)
		return
	}
	sentSteps := make(map[string]bool, len(prevSends))
	for _, s := range prevSends {
		sentSteps[s.Step] = true
	}

	// Find the next step this user needs.
	// If this is our first contact with a user (no prior drip records),
	// skip all already-overdue steps except the most recent one.
	// This prevents spamming users whose trial started before the drip
	// campaign was deployed.
	firstContact := len(prevSends) == 0

	var pendingSteps []step
	for _, s := range steps {
		if sentSteps[s.name] {
			continue
		}
		dueAt := signupTime.Add(s.delay)
		if !r.isDue(now, dueAt, s.delay, u.Region) {
			break // not time yet; don't skip ahead
		}
		pendingSteps = append(pendingSteps, s)
	}

	if len(pendingSteps) == 0 {
		return
	}

	if firstContact && len(pendingSteps) > 1 {
		// Skip all but the most recent due step to avoid a burst of emails.
		for _, s := range pendingSteps[:len(pendingSteps)-1] {
			r.recordSkip(ctx, u.UserID, s.name, "retroactive: drip campaign started after this step was due")
		}
		pendingSteps = pendingSteps[len(pendingSteps)-1:]
	}

	// Evaluate one step per tick.
	r.evaluateStep(ctx, u, pendingSteps[0].name)
}

// regionTimezone returns a *time.Location approximation for a region code.
func regionTimezone(code string) *time.Location {
	var tz string
	switch code {
	case "pdx":
		tz = "America/Los_Angeles"
	case "lax":
		tz = "America/Los_Angeles"
	case "dal":
		tz = "America/Chicago"
	case "nyc":
		tz = "America/New_York"
	case "iad":
		tz = "America/New_York"
	case "fra":
		tz = "Europe/Berlin"
	case "lon":
		tz = "Europe/London"
	case "tyo":
		tz = "Asia/Tokyo"
	case "syd":
		tz = "Australia/Sydney"
	case "sgp":
		tz = "Asia/Singapore"
	default:
		tz = "America/Los_Angeles"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// isDue checks whether a step should fire now.
// For the day0 step (delay < 24h), we just check elapsed time.
// For subsequent steps, we wait until 9 AM in the user's region timezone
// on the appropriate day.
func (r *Runner) isDue(now, dueAt time.Time, stepDelay time.Duration, regionCode string) bool {
	if now.Before(dueAt) {
		return false
	}

	// For short delays (<24h, i.e. day0), fire as soon as the delay passes.
	if stepDelay < 24*time.Hour {
		return true
	}

	// For day-N steps, check that it's past 9 AM in the user's timezone.
	loc := regionTimezone(regionCode)
	localNow := now.In(loc)
	if localNow.Hour() < 9 {
		return false
	}

	return true
}

// evaluateStep decides whether to send or skip a step, then records the outcome.
func (r *Runner) evaluateStep(ctx context.Context, u exedb.ListTrialUsersForDripRow, stepName string) {
	// Gather user activity data.
	boxCount, err := exedb.WithRxRes1(r.db, ctx, (*exedb.Queries).CountBoxesEverForUser, u.UserID)
	if err != nil {
		r.log.ErrorContext(ctx, "drip: failed to count boxes", "user_id", u.UserID, "error", err)
		return
	}
	hasCreatedVM := boxCount > 0

	var subject, body, htmlBody, skipReason string
	var shouldSend bool

	switch stepName {
	case stepDay0Welcome:
		subject, body, htmlBody, skipReason, shouldSend = r.day0Welcome(hasCreatedVM)
	case stepDay1Nudge:
		subject, body, htmlBody, skipReason, shouldSend = r.day1Nudge(ctx, u, hasCreatedVM)
	case stepDay3Feature:
		subject, body, htmlBody, skipReason, shouldSend = r.day3Feature(ctx, u, hasCreatedVM)
	case stepDay5Urgency:
		subject, body, htmlBody, skipReason, shouldSend = r.day5Urgency(hasCreatedVM)
	case stepDay7Expiry:
		subject, body, htmlBody, skipReason, shouldSend = r.day7Expiry()
	case stepDay10WinBack:
		subject, body, htmlBody, skipReason, shouldSend = r.day10WinBack(ctx, u, hasCreatedVM)
	case stepDay14Final:
		subject, body, htmlBody, skipReason, shouldSend = r.day14Final()
	default:
		r.log.ErrorContext(ctx, "drip: unknown step", "step", stepName)
		return
	}

	if shouldSend {
		r.sendAndRecord(ctx, u, stepName, subject, body, htmlBody)
	} else {
		r.recordSkip(ctx, u.UserID, stepName, skipReason)
	}
}

func (r *Runner) sendAndRecord(ctx context.Context, u exedb.ListTrialUsersForDripRow, stepName, subject, body, htmlBody string) {
	from := "David Crawshaw <david@" + r.env.WebHost + ">"

	err := r.send(ctx, email.Message{
		Type:     email.TypeDripCampaign,
		From:     from,
		To:       u.Email,
		Subject:  subject,
		Body:     body,
		HTMLBody: htmlBody,
		ReplyTo:  "david@" + r.env.WebHost,
		Attrs: []slog.Attr{
			slog.String("user_id", u.UserID),
			slog.String("campaign", campaignTrialOnboarding),
			slog.String("step", stepName),
		},
		Attachments: nil,
	})

	status := statSent
	var failReason *string
	if err != nil {
		status = statFailed
		s := err.Error()
		failReason = &s
		r.log.ErrorContext(ctx, "drip: failed to send email",
			"user_id", u.UserID, "step", stepName, "error", err)
	} else {
		r.log.InfoContext(ctx, "drip: email sent",
			"user_id", u.UserID, "email", u.Email, "step", stepName, "subject", subject)
	}

	emailTo := &u.Email
	recordErr := exedb.WithTx1(r.db, ctx, (*exedb.Queries).InsertDripSend, exedb.InsertDripSendParams{
		UserID:       u.UserID,
		Campaign:     campaignTrialOnboarding,
		Step:         stepName,
		Status:       status,
		SkipReason:   failReason,
		EmailTo:      emailTo,
		EmailSubject: &subject,
		EmailBody:    &body,
	})
	if recordErr != nil {
		r.log.ErrorContext(ctx, "drip: failed to record send",
			"user_id", u.UserID, "step", stepName, "error", recordErr)
	}
}

func (r *Runner) recordSkip(ctx context.Context, userID, stepName, reason string) {
	r.log.InfoContext(ctx, "drip: skipped",
		"user_id", userID, "step", stepName, "reason", reason)

	err := exedb.WithTx1(r.db, ctx, (*exedb.Queries).InsertDripSend, exedb.InsertDripSendParams{
		UserID:     userID,
		Campaign:   campaignTrialOnboarding,
		Step:       stepName,
		Status:     statSkipped,
		SkipReason: &reason,
	})
	if err != nil {
		r.log.ErrorContext(ctx, "drip: failed to record skip",
			"user_id", userID, "step", stepName, "error", err)
	}
}

func (r *Runner) webURL(path string) string {
	scheme := "https"
	if r.env.WebHost == "localhost" {
		scheme = "http"
	}
	return scheme + "://" + r.env.WebHost + path
}

func signature() string {
	return "\n-- \nDavid Crawshaw\nCEO exe.dev\n"
}

func htmlSignature() string {
	return `<p style="color:#999">-- <br>David Crawshaw<br>CEO exe.dev<br>` +
		`<a href="{{{pm:unsubscribe}}}" style="color:#999;text-decoration:none">Unsubscribe</a></p>`
}

// htmlLink renders a URL as a plain-looking link: the visible text is the URL itself.
func htmlLink(url string) string {
	return `<a href="` + url + `">` + url + `</a>`
}

// htmlWrap wraps body content in a minimal HTML document.
func htmlWrap(body string) string {
	return `<html><body>` + body + `</body></html>`
}

// SendTestEmail sends the day0 welcome email to the given address for testing.
func (r *Runner) SendTestEmail(ctx context.Context, to string) error {
	subject, body, htmlBody, _, _ := r.day0Welcome(false)
	from := "David Crawshaw <david@" + r.env.WebHost + ">"
	return r.send(ctx, email.Message{
		Type:        email.TypeDripCampaign,
		From:        from,
		To:          to,
		Subject:     subject,
		Body:        body,
		HTMLBody:    htmlBody,
		ReplyTo:     "david@" + r.env.WebHost,
		Attrs:       []slog.Attr{slog.String("test", "true")},
		Attachments: nil,
	})
}

// --- Step implementations ---

func (r *Runner) day0Welcome(hasCreatedVM bool) (subject, body, htmlBody, skipReason string, shouldSend bool) {
	if hasCreatedVM {
		return "", "", "", "user already created a VM", false
	}
	ideaURL := r.webURL("/idea")
	subject = "exe.dev: ready to build"
	body = "Hello,\n\n" +
		"Welcome to exe.dev. You have a 7-day trial to create and use virtual machines.\n\n" +
		"To get started, open a terminal and run:\n\n" +
		"  ssh exe.dev\n\n" +
		"Then you can create your first machine by typing `new`.\n\n" +
		"If you want some inspiration, check out:\n" +
		ideaURL + "\n" +
		signature()
	htmlBody = htmlWrap(
		`<p>Hello,</p>` +
			`<p>Welcome to exe.dev. You have a 7-day trial to create and use virtual machines.</p>` +
			`<p>To get started, open a terminal and run:</p>` +
			`<pre>  ssh exe.dev</pre>` +
			"<p>Then you can create your first machine by typing <code>new</code>.</p>" +
			`<p>If you want some inspiration, check out:<br>` + htmlLink(ideaURL) + `</p>` +
			htmlSignature())
	return subject, body, htmlBody, "", true
}

func (r *Runner) day1Nudge(ctx context.Context, u exedb.ListTrialUsersForDripRow, hasCreatedVM bool) (subject, body, htmlBody, skipReason string, shouldSend bool) {
	if hasCreatedVM {
		return "", "", "", "user already created a VM; on track", false
	}
	ideaURL := r.webURL("/idea")
	subject = "exe.dev: 6 days left, start something"
	body = "Hello,\n\n" +
		"You signed up for exe.dev but haven't created a machine yet. " +
		"Your trial expires in 6 days.\n\n" +
		"Not sure what to build? Here are some ideas:\n" +
		ideaURL + "\n\n" +
		signature()
	htmlBody = htmlWrap(
		`<p>Hello,</p>` +
			`<p>You signed up for exe.dev but haven't created a machine yet. Your trial expires in 6 days.</p>` +
			`<p>Not sure what to build? Here are some ideas:<br>` + htmlLink(ideaURL) + `</p>` +
			htmlSignature())
	return subject, body, htmlBody, "", true
}

func (r *Runner) day3Feature(ctx context.Context, u exedb.ListTrialUsersForDripRow, hasCreatedVM bool) (subject, body, htmlBody, skipReason string, shouldSend bool) {
	if !hasCreatedVM {
		return "", "", "", "user has not created a VM; not sending feature email to inactive user", false
	}

	// Pick a feature they haven't used yet.
	hasShared, _ := exedb.WithRxRes1(r.db, ctx, (*exedb.Queries).HasUserUsedShareLinks, u.UserID)
	hasUsedShelley, _ := exedb.WithRxRes1(r.db, ctx, (*exedb.Queries).HasUserUsedShelley, u.UserID)

	switch {
	case hasUsedShelley == 0:
		shelleyURL := r.webURL("/docs/shelley")
		subject = "exe.dev machines have an agent"
		body = "Hello,\n\n" +
			"Every exe.dev machine comes with Shelley. Credits included.\n\n" +
			"Try it out at exe.dev, click on the shelley icon next to your VM.\n\n" +
			"Learn more:\n" +
			shelleyURL + "\n" +
			signature()
		htmlBody = htmlWrap(
			`<p>Hello,</p>` +
				`<p>Every exe.dev machine comes with Shelley. Credits included.</p>` +
				`<p>Try it out at exe.dev, click on the shelley icon next to your VM.</p>` +
				`<p>Learn more:<br>` + htmlLink(shelleyURL) + `</p>` +
				htmlSignature())
	case hasShared == 0:
		subject = "Share your exe.dev machine with a friend"
		body = "Hello,\n\n" +
			"You can share any of your exe.dev machines with a link. " +
			"Great for pair programming, demos, or getting help.\n\n" +
			"SSH into exe.dev and run:\n\n" +
			"  share\n\n" +
			"Send the link to anyone.\n" +
			signature()
		htmlBody = htmlWrap(
			`<p>Hello,</p>` +
				`<p>You can share any of your exe.dev machines with a link. Great for pair programming, demos, or getting help.</p>` +
				`<p>SSH into exe.dev and run:</p>` +
				`<pre>  share</pre>` +
				`<p>Send the link to anyone.</p>` +
				htmlSignature())
	default:
		docsURL := r.webURL("/docs")
		// They've used the features we track. Send a general tip.
		subject = "Getting the most out of exe.dev"
		body = "Hello,\n\n" +
			"A few things you might not have tried yet:\n\n" +
			"• Custom domains: put a domain name on your machine\n" +
			"• GitHub integration: let agents in a VM access GitHub without leaking secrets\n\n" +
			"Docs: " + docsURL + "\n" +
			signature()
		htmlBody = htmlWrap(
			`<p>Hello,</p>` +
				`<p>A few things you might not have tried yet:</p>` +
				`<ul>` +
				`<li>Custom domains: put a domain name on your machine</li>` +
				`<li>GitHub integration: let agents in a VM access GitHub without leaking secrets</li>` +
				`</ul>` +
				`<p>Docs: ` + htmlLink(docsURL) + `</p>` +
				htmlSignature())
	}
	return subject, body, htmlBody, "", true
}

func (r *Runner) day5Urgency(hasCreatedVM bool) (subject, body, htmlBody, skipReason string, shouldSend bool) {
	userURL := r.webURL("/user")
	subject = "Your trial ends in 2 days"
	if hasCreatedVM {
		body = "Hello,\n\n" +
			"Your exe.dev trial ends in 2 days.\n\n" +
			"Upgrade now to keep your work:\n" +
			userURL + "\n" +
			signature()
		htmlBody = htmlWrap(
			`<p>Hello,</p>` +
				`<p>Your exe.dev trial ends in 2 days.</p>` +
				`<p>Upgrade now to keep your work:<br>` + htmlLink(userURL) + `</p>` +
				htmlSignature())
	} else {
		body = "Hello,\n\n" +
			"You still have 2 days left on your exe.dev trial.\n\n" +
			"If you haven't had a chance to try it yet, you can start right now:\n\n" +
			"  ssh exe.dev\n\n" +
			"It takes about 30 seconds to create your first machine.\n" +
			signature()
		htmlBody = htmlWrap(
			`<p>Hello,</p>` +
				`<p>You still have 2 days left on your exe.dev trial.</p>` +
				`<p>If you haven't had a chance to try it yet, you can start right now:</p>` +
				`<pre>  ssh exe.dev</pre>` +
				`<p>It takes about 30 seconds to create your first machine.</p>` +
				htmlSignature())
	}
	return subject, body, htmlBody, "", true
}

func (r *Runner) day7Expiry() (subject, body, htmlBody, skipReason string, shouldSend bool) {
	userURL := r.webURL("/user")
	subject = "Your trial has ended"
	body = "Hello,\n\n" +
		"Your exe.dev trial has ended. You are now on the Basic plan.\n\n" +
		"Your VMs are stopped, but your disk is preserved for 30 days.\n\n" +
		"Upgrade to the Individual plan to turn your VMs back on. " +
		"New subscribers receive a $100 LLM credit.\n\n" +
		"Upgrade: " + userURL + "\n" +
		signature()
	htmlBody = htmlWrap(
		`<p>Hello,</p>` +
			`<p>Your exe.dev trial has ended. You are now on the Basic plan.</p>` +
			`<p>Your VMs are stopped, but your disk is preserved for 30 days.</p>` +
			`<p>Upgrade to the Individual plan to turn your VMs back on. New subscribers receive a $100 LLM credit.</p>` +
			`<p>Upgrade: ` + htmlLink(userURL) + `</p>` +
			htmlSignature())
	return subject, body, htmlBody, "", true
}

func (r *Runner) day10WinBack(ctx context.Context, u exedb.ListTrialUsersForDripRow, hasCreatedVM bool) (subject, body, htmlBody, skipReason string, shouldSend bool) {
	if !hasCreatedVM {
		return "", "", "", "user never created a VM; no workspace to win back", false
	}
	userURL := r.webURL("/user")
	subject = "Your exe.dev workspace is still here"
	body = "Hello,\n\n" +
		"Your exe.dev workspace and persistent disk are still intact. " +
		"Everything you built during your trial is waiting for you.\n\n" +
		"Upgrade to pick up where you left off:\n" +
		userURL + "\n" +
		signature()
	htmlBody = htmlWrap(
		`<p>Hello,</p>` +
			`<p>Your exe.dev workspace and persistent disk are still intact. Everything you built during your trial is waiting for you.</p>` +
			`<p>Upgrade to pick up where you left off:<br>` + htmlLink(userURL) + `</p>` +
			htmlSignature())
	return subject, body, htmlBody, "", true
}

func (r *Runner) day14Final() (subject, body, htmlBody, skipReason string, shouldSend bool) {
	userURL := r.webURL("/user")
	subject = "Last note from us"
	body = "Hello,\n\n" +
		"This is our last email about your exe.dev trial.\n\n" +
		"Your workspace will be cleaned up soon. " +
		"Upgrade anytime to pick up where you left off:\n" +
		userURL + "\n\n" +
		"Thanks for trying exe.dev.\n" +
		signature()
	htmlBody = htmlWrap(
		`<p>Hello,</p>` +
			`<p>This is our last email about your exe.dev trial.</p>` +
			`<p>Your workspace will be cleaned up soon. Upgrade anytime to pick up where you left off:<br>` + htmlLink(userURL) + `</p>` +
			`<p>Thanks for trying exe.dev.</p>` +
			htmlSignature())
	return subject, body, htmlBody, "", true
}
