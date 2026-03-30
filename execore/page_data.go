package execore

// Page data structs for Vue pages served by renderPage.
// JSON field names match form POST parameter names where applicable,
// so the test infrastructure can round-trip: extract from page → POST as form.

// AuthFormPage is the login/signup form (/auth).
type AuthFormPage struct {
	FormAction      string `json:"formAction"`
	WebHost         string `json:"webHost"`
	Redirect        string `json:"redirect,omitempty"`
	ReturnHost      string `json:"return_host,omitempty"`
	LoginWithExe    bool   `json:"login_with_exe,omitempty"`
	SSHCommand      string `json:"sshCommand,omitempty"`
	Hostname        string `json:"hostname,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Image           string `json:"image,omitempty"`
	Invite          string `json:"invite,omitempty"`
	InviteValid     bool   `json:"inviteValid,omitempty"`
	InviteInvalid   bool   `json:"inviteInvalid,omitempty"`
	InvitePlanType  string `json:"invitePlanType,omitempty"`
	TeamInvite      string `json:"team_invite,omitempty"`
	TeamInviteName  string `json:"teamInviteName,omitempty"`
	TeamInviteEmail string `json:"teamInviteEmail,omitempty"`
	ResponseMode    string `json:"response_mode,omitempty"`
	CallbackURI     string `json:"callback_uri,omitempty"`
}

// AuthPowPage is the proof-of-work challenge page.
type AuthPowPage struct {
	FormAction    string `json:"formAction"`
	Email         string `json:"email"`
	PowToken      string `json:"powToken"`
	PowDifficulty int    `json:"powDifficulty"`
	Redirect      string `json:"redirect,omitempty"`
	ReturnHost    string `json:"return_host,omitempty"`
	LoginWithExe  bool   `json:"login_with_exe,omitempty"`
	Invite        string `json:"invite,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
	Image         string `json:"image,omitempty"`
	TeamInvite    string `json:"team_invite,omitempty"`
	ResponseMode  string `json:"response_mode,omitempty"`
	CallbackURI   string `json:"callback_uri,omitempty"`
}

// EmailVerificationFormPage is the auto-submit email verification form.
type EmailVerificationFormPage struct {
	FormAction string `json:"formAction"`
	Token      string `json:"token"`
	Redirect   string `json:"redirect,omitempty"`
	ReturnHost string `json:"return_host,omitempty"`
	Email      string `json:"email"`
	Source     string `json:"source,omitempty"`
}

// DeviceVerificationPage is the SSH key authorization form.
type DeviceVerificationPage struct {
	FormAction string `json:"formAction"`
	Email      string `json:"email"`
	PublicKey  string `json:"publicKey"`
	Token      string `json:"token"`
}

// EmailVerifiedPage is the post-verification success page.
type EmailVerifiedPage struct {
	Email        string `json:"email"`
	IsWelcome    bool   `json:"isWelcome"`
	Source       string `json:"source"`
	HasPasskeys  bool   `json:"hasPasskeys"`
	NeedsBilling bool   `json:"needsBilling"`
	BillingToken string `json:"billingToken,omitempty"`
}

// BillingSuccessPage is the payment confirmation page.
type BillingSuccessPage struct {
	WebHost string `json:"webHost"`
	Source  string `json:"source"`
}

// AuthErrorPage is the authentication error page.
type AuthErrorPage struct {
	Message     string `json:"message"`
	Command     string `json:"command,omitempty"`
	QueryString string `json:"queryString,omitempty"`
	TraceID     string `json:"traceId,omitempty"`
}

// EmailSentPage is the "check your email" page.
type EmailSentPage struct {
	DevURL string `json:"devUrl,omitempty"`
}

// ProxyLoggedOutPage is the logged-out confirmation page.
type ProxyLoggedOutPage struct {
	WebHost string `json:"webHost"`
}

// LoginConfirmationPage is the proxy login confirmation page.
type LoginConfirmationPage struct {
	WebHost    string `json:"webHost"`
	UserEmail  string `json:"userEmail"`
	SiteDomain string `json:"siteDomain"`
	CancelURL  string `json:"cancelUrl"`
	ConfirmURL string `json:"confirmUrl"`
}

// AppTokenCodeEntryPage is the code entry page for app token auth.
type AppTokenCodeEntryPage struct {
	FormAction string `json:"formAction"`
	Email      string `json:"email"`
	DevCode    string `json:"devCode,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AppTokenSuccessPage is the app token success page.
type AppTokenSuccessPage struct {
	Email       string `json:"email"`
	CallbackURL string `json:"callbackUrl"`
	IsWelcome   bool   `json:"isWelcome"`
	HasPasskeys bool   `json:"hasPasskeys"`
}

// DiscordLinkedPage is the Discord account linked confirmation page.
type DiscordLinkedPage struct {
	DiscordUsername string `json:"discordUsername"`
}

// GithubConnectedPage is the GitHub account connected confirmation page.
type GithubConnectedPage struct {
	GitHubLogin string `json:"gitHubLogin"`
}
