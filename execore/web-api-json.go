package execore

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/plan"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/region"
	"exe.dev/stage"
)

// JSON API types for the Vue dashboard

type jsonBoxInfo struct {
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Image           string          `json:"image"`
	Region          string          `json:"region"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	SSHCommand      string          `json:"sshCommand"`
	ProxyURL        string          `json:"proxyURL"`
	TerminalURL     string          `json:"terminalURL"`
	ShelleyURL      string          `json:"shelleyURL"`
	VSCodeURL       string          `json:"vscodeURL"`
	ProxyPort       int             `json:"proxyPort"`
	ProxyShare      string          `json:"proxyShare"`
	RouteKnown      bool            `json:"routeKnown"`
	SharedUserCount int64           `json:"sharedUserCount"`
	ShareLinkCount  int64           `json:"shareLinkCount"`
	TotalShareCount int64           `json:"totalShareCount"`
	SharedEmails    []string        `json:"sharedEmails"`
	ShareLinks      []jsonShareLink `json:"shareLinks"`
	DisplayTags     []string        `json:"displayTags"`
	HasCreationLog  bool            `json:"hasCreationLog"`
	IsTeamShared    bool            `json:"isTeamShared"`
}

type jsonShareLink struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

type jsonSharedBox struct {
	Name       string `json:"name"`
	OwnerEmail string `json:"ownerEmail"`
	ProxyURL   string `json:"proxyURL"`
}

type jsonTeamBox struct {
	Name         string   `json:"name"`
	CreatorEmail string   `json:"creatorEmail"`
	Status       string   `json:"status"`
	ProxyURL     string   `json:"proxyURL"`
	SSHCommand   string   `json:"sshCommand"`
	DisplayTags  []string `json:"displayTags"`
}

type jsonTeamSharedBox struct {
	Name        string   `json:"name"`
	OwnerEmail  string   `json:"ownerEmail"`
	Status      string   `json:"status"`
	ProxyURL    string   `json:"proxyURL"`
	SSHCommand  string   `json:"sshCommand"`
	DisplayTags []string `json:"displayTags"`
}

type jsonDashboardData struct {
	User               jsonUserInfo        `json:"user"`
	Boxes              []jsonBoxInfo       `json:"boxes"`
	SharedBoxes        []jsonSharedBox     `json:"sharedBoxes"`
	TeamSharedBoxes    []jsonTeamSharedBox `json:"teamSharedBoxes"`
	TeamBoxes          []jsonTeamBox       `json:"teamBoxes"`
	HasTeam            bool                `json:"hasTeam"`
	InviteCount        int64               `json:"inviteCount"`
	CanRequestInvites  bool                `json:"canRequestInvites"`
	SSHCommand         string              `json:"sshCommand"`
	ReplHost           string              `json:"replHost"`
	ShowIntegrations   bool                `json:"showIntegrations"`
	BillingPeriodStart time.Time           `json:"billingPeriodStart"`
	BillingPeriodEnd   time.Time           `json:"billingPeriodEnd"`
}

type jsonUserInfo struct {
	Email                string `json:"email"`
	Region               string `json:"region"`
	RegionDisplay        string `json:"regionDisplay"`
	NewsletterSubscribed bool   `json:"newsletterSubscribed"`
}

func regionDisplay(code string) string {
	if code == "" {
		return ""
	}
	r, err := region.ByCode(code)
	if err != nil {
		slog.Warn("unknown region code", "code", code)
		return ""
	}
	return r.Display
}

func newJSONUserInfo(user exedb.User) jsonUserInfo {
	return jsonUserInfo{
		Email:         user.Email,
		Region:        user.Region,
		RegionDisplay: regionDisplay(user.Region),
	}
}

func newJSONUserInfoWithNewsletter(user exedb.User) jsonUserInfo {
	info := newJSONUserInfo(user)
	info.NewsletterSubscribed = user.NewsletterSubscribed
	return info
}

type jsonSSHKey struct {
	PublicKey     string  `json:"publicKey"`
	Comment       string  `json:"comment"`
	Fingerprint   string  `json:"fingerprint"`
	AddedAt       *string `json:"addedAt"`
	LastUsedAt    *string `json:"lastUsedAt"`
	IntegrationID *string `json:"integrationId,omitempty"`
	ApiKeyHint    *string `json:"apiKeyHint,omitempty"`
}

type jsonPasskey struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
}

type jsonSiteSession struct {
	Domain     string  `json:"domain"`
	URL        string  `json:"url"`
	LastUsedAt *string `json:"lastUsedAt"`
}

type jsonTeamInfo struct {
	DisplayName    string           `json:"displayName"`
	Role           string           `json:"role"`
	IsAdmin        bool             `json:"isAdmin"`
	IsBillingOwner bool             `json:"isBillingOwner"`
	OnlyMember     bool             `json:"onlyMember"`
	Members        []jsonTeamMember `json:"members"`
	BoxCount       int64            `json:"boxCount"`
	MaxBoxes       int              `json:"maxBoxes"`
}

type jsonTeamMember struct {
	Email    string  `json:"email"`
	Role     string  `json:"role"`
	JoinedAt *string `json:"joinedAt"`
}

type jsonPendingInvite struct {
	Token     string `json:"token"`
	TeamName  string `json:"teamName"`
	InvitedBy string `json:"invitedBy"`
	VMCount   int64  `json:"vmCount"`
}

type jsonPaymentMethod struct {
	Type         string `json:"type"`
	Brand        string `json:"brand,omitempty"`
	Last4        string `json:"last4,omitempty"`
	ExpMonth     int    `json:"expMonth,omitempty"`
	ExpYear      int    `json:"expYear,omitempty"`
	Email        string `json:"email,omitempty"`
	DisplayLabel string `json:"displayLabel"`
}

type jsonCreditInfo struct {
	PlanName                   string             `json:"planName"`
	SelfServeBilling           bool               `json:"selfServeBilling"`
	PaidPlan                   bool               `json:"paidPlan"`
	SkipBilling                bool               `json:"skipBilling"`
	BillingStatus              string             `json:"billingStatus"`
	ShelleyCreditsAvailable    float64            `json:"shelleyCreditsAvailable"`
	ShelleyCreditsMax          float64            `json:"shelleyCreditsMax"`
	ExtraCreditsUSD            float64            `json:"extraCreditsUSD"`
	TotalCreditsUSD            float64            `json:"totalCreditsUSD"`
	TotalRemainingPct          float64            `json:"totalRemainingPct"`
	MonthlyAvailableUSD        float64            `json:"monthlyAvailableUSD"`
	MonthlyUsedUSD             float64            `json:"monthlyUsedUSD"`
	MonthlyUsedPct             float64            `json:"monthlyUsedPct"`
	UsedCreditsUSD             float64            `json:"usedCreditsUSD"`
	TotalCapacityUSD           float64            `json:"totalCapacityUSD"`
	UsedBarPct                 float64            `json:"usedBarPct"`
	HasShelleyFreeCreditPct    bool               `json:"hasShelleyFreeCreditPct"`
	MonthlyCreditsResetAt      string             `json:"monthlyCreditsResetAt"`
	LedgerBalanceUSD           float64            `json:"ledgerBalanceUSD"`
	Purchases                  []jsonPurchaseRow  `json:"purchases"`
	Gifts                      []jsonGiftRow      `json:"gifts"`
	Invoices                   []jsonInvoiceRow   `json:"invoices"`
	PaymentMethod              *jsonPaymentMethod `json:"paymentMethod"`
	PaymentMethodManagedByTeam bool               `json:"paymentMethodManagedByTeam,omitempty"`
}

type jsonPurchaseRow struct {
	Amount     string `json:"amount"`
	Date       string `json:"date"`
	ReceiptURL string `json:"receiptURL"`
}

type jsonGiftRow struct {
	Amount string `json:"amount"`
	Reason string `json:"reason"`
	Date   string `json:"date"`
}

type jsonInvoiceRow struct {
	Description      string `json:"description"`
	PlanName         string `json:"planName"`
	PeriodStart      string `json:"periodStart"`
	PeriodEnd        string `json:"periodEnd"`
	Date             string `json:"date"`
	Amount           string `json:"amount"` // formatted e.g. "20.00"
	Status           string `json:"status"` // "paid", "open", etc.
	HostedInvoiceURL string `json:"hostedInvoiceURL"`
	InvoicePDF       string `json:"invoicePDF"`
}

type jsonRegionOption struct {
	Code    string `json:"code"`
	Display string `json:"display"`
}

type jsonProfileData struct {
	User               jsonUserInfo        `json:"user"`
	SSHKeys            []jsonSSHKey        `json:"sshKeys"`
	Passkeys           []jsonPasskey       `json:"passkeys"`
	SiteSessions       []jsonSiteSession   `json:"siteSessions"`
	SharedBoxes        []jsonSharedBox     `json:"sharedBoxes"`
	Boxes              []jsonBoxMinimal    `json:"boxes"`
	TeamInfo           *jsonTeamInfo       `json:"teamInfo"`
	PendingTeamInvites []jsonPendingInvite `json:"pendingTeamInvites"`
	CanEnableTeam      bool                `json:"canEnableTeam"`
	Credits            jsonCreditInfo      `json:"credits"`
	BasicUser          bool                `json:"basicUser"`
	ShowIntegrations   bool                `json:"showIntegrations"`
	InviteCount        int64               `json:"inviteCount"`
	CanRequestInvites  bool                `json:"canRequestInvites"`
	AvailableRegions   []jsonRegionOption  `json:"availableRegions"`
}

type jsonIntegrationInfo struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Target       string   `json:"target"`
	HasHeader    bool     `json:"hasHeader"`
	HasBasicAuth bool     `json:"hasBasicAuth"`
	Repositories []string `json:"repositories"`
	Attachments  []string `json:"attachments"`
	IsTeam       bool     `json:"isTeam"`
	PeerVM       string   `json:"peerVM,omitempty"`
}

type jsonGitHubAccount struct {
	GitHubLogin    string `json:"githubLogin"`
	TargetLogin    string `json:"targetLogin"`
	InstallationID int64  `json:"installationID"`
}

type jsonIntegrationsData struct {
	Integrations       []jsonIntegrationInfo `json:"integrations"`
	GitHubIntegrations []jsonIntegrationInfo `json:"githubIntegrations"`
	ProxyIntegrations  []jsonIntegrationInfo `json:"proxyIntegrations"`
	GitHubAccounts     []jsonGitHubAccount   `json:"githubAccounts"`
	GitHubEnabled      bool                  `json:"githubEnabled"`
	GitHubAppSlug      string                `json:"githubAppSlug"`
	HasPushTokens      bool                  `json:"hasPushTokens"`
	AllTags            []string              `json:"allTags"`
	TagVMs             map[string][]string   `json:"tagVMs"`
	Boxes              []jsonBoxMinimal      `json:"boxes"`
	IntegrationScheme  string                `json:"integrationScheme"`
	BoxHost            string                `json:"boxHost"`
	HasTeam            bool                  `json:"hasTeam"`
}

type jsonBoxMinimal struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func writeJSONOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, v)
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func formatRFC3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// handleAPIDashboard returns JSON data for the VM list page.
func (s *Server) handleAPIDashboard(w http.ResponseWriter, r *http.Request, userID string) {
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// If there are active creation streams, wait for the boxes to appear in the DB.
	// After creating a VM, the user is redirected to /?filter=<hostname>, but the box
	// may not have been inserted into the DB yet. Poll until all actively-being-created
	// boxes appear so the dashboard shows them with status="creating".
	creatingHostnames := s.getActiveCreationHostnames(userID)
	deadline := time.Now().Add(5 * time.Second)
	for len(creatingHostnames) > 0 && time.Now().Before(deadline) {
		var stillMissing []string
		for _, hostname := range creatingHostnames {
			exists, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxWithNameExists, hostname)
			if err != nil || exists == 0 {
				stillMissing = append(stillMissing, hostname)
			}
		}
		if len(stillMissing) == 0 {
			break
		}
		creatingHostnames = stillMissing
		time.Sleep(100 * time.Millisecond)
	}

	// Get boxes
	boxResults, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesForUserDashboard, user.UserID)
	if err != nil {
		if !errors.Is(r.Context().Err(), context.Canceled) {
			s.slog().ErrorContext(r.Context(), "Failed to get boxes for API dashboard", "error", err)
		}
	}

	// Batch-fetch all sharing data for the user's boxes (3 count queries + 3 list queries)
	// instead of 6 queries per box (N+1 elimination).
	pendingCounts, activeShareCounts, linkCounts, pendingEmails, activeEmails, shareLinksAll := s.batchShareData(r.Context(), user.UserID)

	// Batch-fetch which of the user's boxes are team-shared
	teamSharedBoxIDs, _ := withRxRes1(s, r.Context(), (*exedb.Queries).ListTeamSharedBoxIDsForUser, user.UserID)
	teamSharedSet := make(map[int64]bool, len(teamSharedBoxIDs))
	for _, id := range teamSharedBoxIDs {
		teamSharedSet[id] = true
	}

	boxes := make([]jsonBoxInfo, 0, len(boxResults))
	for _, result := range boxResults {
		box := exedb.Box{
			ID:              result.ID,
			CreatedByUserID: result.CreatedByUserID,
			Name:            result.Name,
			Status:          result.Status,
			Image:           result.Image,
			CreatedAt:       result.CreatedAt,
			UpdatedAt:       result.UpdatedAt,
			LastStartedAt:   result.LastStartedAt,
			Routes:          result.Routes,
			Region:          result.Region,
			Tags:            result.Tags,
		}
		if result.ContainerID != "" {
			box.ContainerID = &result.ContainerID
		}
		if result.CreationLog != "" {
			box.CreationLog = &result.CreationLog
		}

		route := box.GetRoute()
		boxID := int64(box.ID)
		sharedUserCount := pendingCounts[boxID] + activeShareCounts[boxID]
		shareLinkCount := linkCounts[boxID]

		// Merge pending + active share emails for this box
		var sharedEmails []string
		sharedEmails = append(sharedEmails, pendingEmails[boxID]...)
		sharedEmails = append(sharedEmails, activeEmails[boxID]...)

		// Build share links for this box
		var shareLinks []jsonShareLink
		for _, sl := range shareLinksAll[boxID] {
			shareLinks = append(shareLinks, jsonShareLink{
				Token: sl.Token,
				URL:   sl.URL,
			})
		}

		var shelleyURL string
		if strings.Contains(result.Image, "exeuntu") {
			shelleyURL = s.shelleyURL(result.Name)
		}

		boxes = append(boxes, jsonBoxInfo{
			Name:            result.Name,
			Status:          result.Status,
			Image:           result.Image,
			Region:          result.Region,
			CreatedAt:       result.CreatedAt.Format("Jan 2, 2006 15:04 MST"),
			UpdatedAt:       formatRFC3339(result.UpdatedAt),
			SSHCommand:      s.boxSSHConnectionCommand(result.Name),
			ProxyURL:        s.boxProxyAddress(result.Name),
			TerminalURL:     s.xtermURL(result.Name, r.TLS != nil),
			ShelleyURL:      shelleyURL,
			VSCodeURL:       string(s.vscodeURL(result.Name)),
			ProxyPort:       route.Port,
			ProxyShare:      route.Share,
			RouteKnown:      box.Routes != nil && *box.Routes != "",
			SharedUserCount: sharedUserCount,
			ShareLinkCount:  shareLinkCount,
			TotalShareCount: sharedUserCount + shareLinkCount,
			SharedEmails:    nonNil(sharedEmails),
			ShareLinks:      nonNil(shareLinks),
			DisplayTags:     nonNil(box.GetTags()),
			HasCreationLog:  box.CreationLog != nil && *box.CreationLog != "",
			IsTeamShared:    teamSharedSet[int64(box.ID)],
		})
	}

	// Shared boxes
	sharedBoxResults, _ := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesSharedWithUser, user.UserID)
	sharedBoxes := make([]jsonSharedBox, 0, len(sharedBoxResults))
	for _, result := range sharedBoxResults {
		sharedBoxes = append(sharedBoxes, jsonSharedBox{
			Name:       result.Name,
			OwnerEmail: result.OwnerEmail,
			ProxyURL:   s.boxProxyAddress(result.Name),
		})
	}

	// Team-shared boxes (boxes shared with the user's team via `share add <vm> team`)
	teamSharedResults, _ := withRxRes1(s, r.Context(), (*exedb.Queries).ListBoxesSharedWithUserTeam, user.UserID)
	// Build a set of box names from own boxes + individually shared to deduplicate
	ownBoxNames := make(map[string]bool, len(boxes))
	for _, b := range boxes {
		ownBoxNames[b.Name] = true
	}
	sharedBoxNames := make(map[string]bool, len(sharedBoxes))
	for _, b := range sharedBoxes {
		sharedBoxNames[b.Name] = true
	}
	teamSharedBoxes := make([]jsonTeamSharedBox, 0, len(teamSharedResults))
	for _, result := range teamSharedResults {
		if ownBoxNames[result.Name] || sharedBoxNames[result.Name] {
			continue
		}
		teamSharedBoxes = append(teamSharedBoxes, jsonTeamSharedBox{
			Name:        result.Name,
			OwnerEmail:  result.OwnerEmail,
			Status:      result.Status,
			ProxyURL:    s.boxProxyAddress(result.Name),
			SSHCommand:  s.boxSSHConnectionCommand(result.Name),
			DisplayTags: nonNil(parseTags(result.Tags)),
		})
	}

	// Team boxes (admin view of all team members' boxes)
	teamBoxResults, _ := s.ListTeamBoxesForAdmin(r.Context(), user.UserID)
	// Deduplicate: exclude boxes already in team-shared or individually shared
	teamSharedNames := make(map[string]bool, len(teamSharedBoxes))
	for _, b := range teamSharedBoxes {
		teamSharedNames[b.Name] = true
	}
	teamBoxes := make([]jsonTeamBox, 0, len(teamBoxResults))
	for _, result := range teamBoxResults {
		if ownBoxNames[result.Name] || sharedBoxNames[result.Name] || teamSharedNames[result.Name] {
			continue
		}
		teamBoxes = append(teamBoxes, jsonTeamBox{
			Name:         result.Name,
			CreatorEmail: result.CreatorEmail,
			Status:       result.Status,
			ProxyURL:     s.boxProxyAddress(result.Name),
			SSHCommand:   s.boxSSHConnectionCommand(result.Name),
			DisplayTags:  nonNil(parseTags(result.Tags)),
		})
	}

	// Check if user is on a team
	team, _ := s.GetTeamForUser(r.Context(), user.UserID)
	hasTeam := team != nil

	inviteCount, _ := withRxRes1(s, r.Context(), (*exedb.Queries).CountUnusedInviteCodesForUser, &user.UserID)
	canRequestInvites := s.UserHasEntitlement(r.Context(), plan.SourceWeb, plan.InviteRequest, userID)
	showIntegrations := s.showIntegrationsNav(r.Context(), userID)

	planRow, planErr := withRxRes1(s, r.Context(), (*exedb.Queries).GetActivePlanForUser, userID)
	var billingAccountID string
	if planErr == nil {
		billingAccountID = planRow.AccountID
	}
	periodStart, periodEnd := billingPeriodForUser(r.Context(), s, billingAccountID, planErr)

	writeJSONOK(w, jsonDashboardData{
		User:               newJSONUserInfo(user),
		Boxes:              boxes,
		SharedBoxes:        sharedBoxes,
		TeamSharedBoxes:    teamSharedBoxes,
		TeamBoxes:          teamBoxes,
		HasTeam:            hasTeam,
		InviteCount:        inviteCount,
		CanRequestInvites:  canRequestInvites,
		SSHCommand:         s.replSSHConnectionCommand(),
		ReplHost:           s.env.ReplHost,
		ShowIntegrations:   showIntegrations,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
	})
}

// handleAPIProfile returns JSON data for the profile page.
func (s *Server) handleAPIProfile(w http.ResponseWriter, r *http.Request, userID string) {
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// SSH keys
	var sshKeys []jsonSSHKey
	_ = s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		dbKeys, err := queries.GetSSHKeysForUser(ctx, user.UserID)
		if err != nil {
			return err
		}
		for _, dbKey := range dbKeys {
			sshKeys = append(sshKeys, jsonSSHKey{
				PublicKey:     dbKey.PublicKey,
				Comment:       dbKey.Comment,
				Fingerprint:   dbKey.Fingerprint,
				AddedAt:       formatTimePtr(dbKey.AddedAt),
				LastUsedAt:    formatTimePtr(dbKey.LastUsedAt),
				IntegrationID: dbKey.IntegrationID,
				ApiKeyHint:    dbKey.ApiKeyHint,
			})
		}
		return nil
	})

	// Passkeys
	passkeysRaw, _ := s.getPasskeysForUser(r.Context(), userID)
	passkeys := make([]jsonPasskey, 0, len(passkeysRaw))
	for _, pk := range passkeysRaw {
		passkeys = append(passkeys, jsonPasskey{
			ID:         pk.ID,
			Name:       pk.Name,
			CreatedAt:  pk.CreatedAt,
			LastUsedAt: pk.LastUsedAt,
		})
	}

	// Site sessions
	var siteSessions []jsonSiteSession
	siteCookies, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetSiteCookiesForUser, exedb.GetSiteCookiesForUserParams{
		UserID: userID,
		Domain: s.env.WebHost,
	})
	if err == nil {
		seenDomains := make(map[string]bool)
		for _, cookie := range siteCookies {
			if seenDomains[cookie.Domain] {
				continue
			}
			seenDomains[cookie.Domain] = true
			siteSessions = append(siteSessions, jsonSiteSession{
				Domain:     cookie.Domain,
				URL:        "https://" + cookie.Domain,
				LastUsedAt: formatTimePtr(cookie.LastUsedAt),
			})
		}
	}

	// Shared boxes
	sharedBoxResults, _ := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesSharedWithUser, user.UserID)
	sharedBoxes := make([]jsonSharedBox, 0, len(sharedBoxResults))
	for _, result := range sharedBoxResults {
		sharedBoxes = append(sharedBoxes, jsonSharedBox{
			Name:       result.Name,
			OwnerEmail: result.OwnerEmail,
			ProxyURL:   s.boxProxyAddress(result.Name),
		})
	}

	// Basic user check
	basicUser := s.isBasicUser(r.Context(), user, len(sshKeys))

	// Billing
	var planName string
	var selfServeBilling bool
	var paidPlan bool
	var billingStatus string
	skipBilling := s.env.SkipBilling
	if planRow, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetActivePlanForUser, userID); err == nil {
		version := plan.Base(planRow.PlanID)
		planName = plan.Name(version)
		selfServeBilling = version == plan.CategoryIndividual
		paidPlan = plan.IsPaid(version)
	}
	billingRow, billingErr := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBilling, userID)
	if billingErr == nil {
		billingStatus = billingRow.BillingStatus
	}

	// Credits
	creditBalance := tender.Zero()
	var purchases []jsonPurchaseRow
	account, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err == nil {
		balance, err := s.billing.CreditBalance(r.Context(), account.ID)
		if err == nil {
			creditBalance = balance
		}

		cutoff := time.Now().AddDate(0, -6, 0)
		credits, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListBillingCreditsForAccount, account.ID)
		if err == nil {
			receiptURLs, _ := s.billing.ReceiptURLs(r.Context(), account.ID)
			for _, c := range credits {
				if c.Amount > 0 && c.StripeEventID != nil && c.CreatedAt.After(cutoff) {
					cr := c.Amount / 1_000_000
					p := jsonPurchaseRow{
						Amount: fmt.Sprintf("%d", cr),
						Date:   c.CreatedAt.Format("02 Jan 2006"),
					}
					if c.StripeEventID != nil && receiptURLs != nil {
						p.ReceiptURL = receiptURLs[*c.StripeEventID]
					}
					purchases = append(purchases, p)
				}
			}
		}
	}

	var shelleyCreditsAvailable, shelleyCreditsMax float64
	var hasShelleyFreeCreditPct bool
	creditState, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserLLMCredit, userID)
	var creditPtr *exedb.UserLlmCredit
	if err == nil {
		creditPtr = &creditState
	}
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		plan, err := llmgateway.PlanForUser(r.Context(), s.db, userID, creditPtr)
		if err == nil && plan.MaxCredit > 0 {
			effectiveAvailable := creditState.AvailableCredit
			if creditPtr == nil {
				effectiveAvailable = plan.MaxCredit
			} else if plan.Refresh != nil {
				effectiveAvailable, _ = plan.Refresh(creditState.AvailableCredit, creditState.LastRefreshAt, time.Now())
			}
			shelleyCreditsAvailable = effectiveAvailable
			shelleyCreditsMax = plan.MaxCredit
			hasShelleyFreeCreditPct = true
		}
	}

	var bonusRemaining, bonusGrantAmount float64
	if creditPtr != nil && creditPtr.BillingUpgradeBonusGranted == 1 {
		bonusGrantAmount = llmgateway.UpgradeBonusCreditUSD
		if shelleyCreditsAvailable > shelleyCreditsMax {
			bonusRemaining = shelleyCreditsAvailable - shelleyCreditsMax
			if bonusRemaining > bonusGrantAmount {
				bonusRemaining = bonusGrantAmount
			}
		}
	}

	var giftCreditsUSD float64
	var giftEntries []billing.GiftEntry
	if account.ID != "" {
		giftEntries, _ = s.billing.ListGifts(r.Context(), account.ID)
		giftCreditsUSD = giftCreditsUSDFromLedger(giftEntries)
	}
	if hasSignupGiftInLedger(giftEntries) {
		shelleyCreditsAvailable = max(shelleyCreditsAvailable-bonusRemaining, 0)
		bonusGrantAmount = 0
		bonusRemaining = 0
	}
	extraCreditsUSD := float64(creditBalance.Microcents())/1_000_000 - giftCreditsUSD
	if extraCreditsUSD < 0 {
		extraCreditsUSD = 0
	}

	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: shelleyCreditsAvailable,
		planMaxCredit:           shelleyCreditsMax,
		bonusRemaining:          bonusRemaining,
		bonusGrantAmount:        bonusGrantAmount,
		extraCreditsUSD:         extraCreditsUSD,
		giftCreditsUSD:          giftCreditsUSD,
	})

	giftRows := buildGiftRows(bonusGrantAmount, giftEntries)
	jsonGifts := make([]jsonGiftRow, len(giftRows))
	for i, g := range giftRows {
		jsonGifts[i] = jsonGiftRow{Amount: g.Amount, Reason: g.Reason, Date: g.Date}
	}

	// Payment method — fetch for self-serve billing users, or from the team billing owner.
	var paymentMethod *jsonPaymentMethod
	var paymentMethodManagedByTeam bool
	if selfServeBilling && account.ID != "" {
		pm, err := s.billing.GetPaymentMethod(r.Context(), account.ID)
		if err != nil {
			s.slog().WarnContext(r.Context(), "failed to get payment method", "error", err, "account_id", account.ID)
		} else if pm != nil {
			paymentMethod = &jsonPaymentMethod{
				Type:         pm.Type,
				Brand:        pm.Brand,
				Last4:        pm.Last4,
				ExpMonth:     pm.ExpMonth,
				ExpYear:      pm.ExpYear,
				Email:        pm.Email,
				DisplayLabel: pm.DisplayLabel,
			}
		}
	}
	// For team members (non-billing-owner), fetch the billing owner's payment method.
	if paymentMethod == nil {
		if ownerAccountID, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetTeamBillingOwnerAccountID, userID); err == nil && ownerAccountID != "" {
			pm, err := s.billing.GetPaymentMethod(r.Context(), ownerAccountID)
			if err != nil {
				s.slog().WarnContext(r.Context(), "failed to get team billing owner payment method", "error", err, "owner_account_id", ownerAccountID)
			} else if pm != nil {
				paymentMethod = &jsonPaymentMethod{
					Type:         pm.Type,
					Brand:        pm.Brand,
					Last4:        pm.Last4,
					ExpMonth:     pm.ExpMonth,
					ExpYear:      pm.ExpYear,
					Email:        pm.Email,
					DisplayLabel: pm.DisplayLabel,
				}
				paymentMethodManagedByTeam = true
			}
		}
	}

	showIntegrations := s.showIntegrationsNav(r.Context(), userID)
	inviteCount, _ := withRxRes1(s, r.Context(), (*exedb.Queries).CountUnusedInviteCodesForUser, &user.UserID)
	canRequestInvites := s.UserHasEntitlement(r.Context(), plan.SourceWeb, plan.InviteRequest, userID)

	// Boxes (for API key VM scope dropdown)
	var boxes []jsonBoxMinimal
	if userBoxes, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxesForUser, userID); err == nil {
		for _, b := range userBoxes {
			boxes = append(boxes, jsonBoxMinimal{Name: b.Name, Status: b.Status})
		}
	}

	profile := jsonProfileData{
		User:               newJSONUserInfoWithNewsletter(user),
		SSHKeys:            nonNil(sshKeys),
		Passkeys:           nonNil(passkeys),
		SiteSessions:       nonNil(siteSessions),
		SharedBoxes:        sharedBoxes,
		Boxes:              nonNil(boxes),
		PendingTeamInvites: make([]jsonPendingInvite, 0),
		BasicUser:          basicUser,
		ShowIntegrations:   showIntegrations,
		InviteCount:        inviteCount,
		CanRequestInvites:  canRequestInvites,
		Credits: jsonCreditInfo{
			PlanName:                   planName,
			SelfServeBilling:           selfServeBilling,
			PaidPlan:                   paidPlan,
			SkipBilling:                skipBilling,
			BillingStatus:              billingStatus,
			ShelleyCreditsAvailable:    shelleyCreditsAvailable,
			ShelleyCreditsMax:          shelleyCreditsMax,
			ExtraCreditsUSD:            extraCreditsUSD,
			TotalCreditsUSD:            max(shelleyCreditsAvailable+extraCreditsUSD+giftCreditsUSD, 0),
			TotalRemainingPct:          bar.totalRemainingPct,
			MonthlyAvailableUSD:        bar.monthlyAvailable,
			MonthlyUsedUSD:             max(shelleyCreditsMax-bar.monthlyAvailable, 0),
			MonthlyUsedPct:             monthlyUsedPct(bar.monthlyAvailable, shelleyCreditsMax),
			UsedCreditsUSD:             bar.usedCreditsUSD,
			TotalCapacityUSD:           bar.totalCapacity,
			UsedBarPct:                 bar.usedBarPct,
			HasShelleyFreeCreditPct:    hasShelleyFreeCreditPct,
			MonthlyCreditsResetAt:      nextUTCMonthStart().Format("15:04 on Jan 2"),
			LedgerBalanceUSD:           max(float64(creditBalance.Microcents())/1_000_000, 0),
			Purchases:                  nonNil(purchases),
			Gifts:                      nonNil(jsonGifts),
			PaymentMethod:              paymentMethod,
			PaymentMethodManagedByTeam: paymentMethodManagedByTeam,
		},
	}

	// Team info
	if team, err := s.GetTeamForUser(r.Context(), userID); err == nil && team != nil {
		profile.Credits.PlanName = "Team"
		ti := &jsonTeamInfo{
			DisplayName:    team.DisplayName,
			Role:           team.Role,
			IsAdmin:        team.Role != "user",
			IsBillingOwner: team.Role == "billing_owner",
		}
		if boxCount, err := withRxRes1(s, r.Context(), (*exedb.Queries).CountTeamBoxes, userID); err == nil {
			ti.BoxCount = boxCount
		}
		if limits, err := s.GetEffectiveLimits(r.Context(), userID); err == nil {
			ti.MaxBoxes = GetMaxTeamBoxes(limits)
		} else {
			ti.MaxBoxes = stage.DefaultMaxTeamBoxes
		}
		if members, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetTeamMembers, team.TeamID); err == nil {
			ti.OnlyMember = len(members) == 1
			for _, m := range members {
				mdi := jsonTeamMember{Email: m.Email, Role: m.Role}
				s := m.JoinedAt.Format(time.RFC3339)
				mdi.JoinedAt = &s
				ti.Members = append(ti.Members, mdi)
			}
		}
		profile.TeamInfo = ti
	} else {
		// Pending invites
		ce := canonicalizeEmail(user.Email)
		if invites, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetPendingTeamInvitesForUser, ce); err == nil {
			var vmCount int64
			if len(invites) > 0 {
				vmCount, _ = withRxRes1(s, r.Context(), (*exedb.Queries).CountBoxesForUser, userID)
			}
			for _, inv := range invites {
				profile.PendingTeamInvites = append(profile.PendingTeamInvites, jsonPendingInvite{
					Token:     inv.Token,
					TeamName:  inv.TeamName,
					InvitedBy: inv.InvitedByEmail,
					VMCount:   vmCount,
				})
			}
		}
		if !basicUser && s.UserHasEntitlement(r.Context(), plan.SourceWeb, plan.TeamCreate, userID) {
			profile.CanEnableTeam = true
		}
	}

	// Invoices — only for billing owners (no team = individual = always; team = billing_owner only)
	canManageBilling := profile.TeamInfo == nil || profile.TeamInfo.IsBillingOwner
	if canManageBilling && account.ID != "" && selfServeBilling {
		toRow := func(inv billing.InvoiceInfo) jsonInvoiceRow {
			return jsonInvoiceRow{
				Description:      inv.Description,
				PlanName:         inv.PlanName,
				PeriodStart:      inv.PeriodStart.Format("Jan 2"),
				PeriodEnd:        inv.PeriodEnd.Format("Jan 2, 2006"),
				Date:             inv.Date.Format("02 Jan 2006"),
				Amount:           fmt.Sprintf("%.2f", float64(inv.AmountPaid)/100),
				Status:           inv.Status,
				HostedInvoiceURL: inv.HostedInvoiceURL,
				InvoicePDF:       inv.InvoicePDF,
			}
		}
		var jsonInvoices []jsonInvoiceRow
		if upcoming, err := s.billing.UpcomingInvoice(r.Context(), account.ID); err == nil && upcoming != nil {
			jsonInvoices = append(jsonInvoices, toRow(*upcoming))
		}
		if invoices, err := s.billing.ListInvoices(r.Context(), account.ID); err == nil {
			for _, inv := range invoices {
				jsonInvoices = append(jsonInvoices, toRow(inv))
			}
		}
		profile.Credits.Invoices = nonNil(jsonInvoices)
	}

	// Available regions.
	available := s.availableRegionsForUser(r.Context(), userID, user.Region)
	profile.AvailableRegions = make([]jsonRegionOption, len(available))
	for i, r := range available {
		profile.AvailableRegions[i] = jsonRegionOption{Code: r.Code, Display: r.Display}
	}

	writeJSONOK(w, profile)
}

// availableRegionsForUser returns the regions the user may select, including any
// regions unlocked via their team's private exelet assignments.
func (s *Server) availableRegionsForUser(ctx context.Context, userID, currentRegionCode string) []region.Region {
	var unlockedCodes []string
	if team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamForUser, userID); err == nil {
		if addrs, err := withRxRes1(s, ctx, (*exedb.Queries).ListTeamExeletsForTeam, team.TeamID); err == nil {
			seen := make(map[string]bool)
			for _, addr := range addrs {
				if r, err := region.ParseExeletRegion(addr); err == nil && !seen[r.Code] {
					seen[r.Code] = true
					unlockedCodes = append(unlockedCodes, r.Code)
				}
			}
		}
	}
	return region.AvailableFor(currentRegionCode, unlockedCodes...)
}

// handleAPIProfileRegion handles POST /api/profile/region to update the user's preferred region.
func (s *Server) handleAPIProfileRegion(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		Region string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	available := s.availableRegionsForUser(r.Context(), userID, user.Region)
	var chosen *region.Region
	for _, r := range available {
		if r.Code == req.Region {
			r := r
			chosen = &r
			break
		}
	}
	if chosen == nil {
		http.Error(w, "region not available", http.StatusBadRequest)
		return
	}

	if err := withTx1(s, r.Context(), (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		UserID: userID,
		Region: chosen.Code,
	}); err != nil {
		http.Error(w, "failed to update region", http.StatusInternalServerError)
		return
	}

	writeJSONOK(w, map[string]string{"region": chosen.Code, "regionDisplay": chosen.Display})
}

// handleAPIIntegrations returns JSON data for the integrations page.
func (s *Server) handleAPIIntegrations(w http.ResponseWriter, r *http.Request, userID string) {
	var integrations []jsonIntegrationInfo
	dbIntegrations, _ := withRxRes1(s, r.Context(), (*exedb.Queries).ListIntegrationsByUser, userID)
	for _, ig := range dbIntegrations {
		info := jsonIntegrationInfo{
			Name:        ig.Name,
			Type:        ig.Type,
			Attachments: nonNil(ig.GetAttachments()),
		}
		switch ig.Type {
		case "http-proxy":
			var cfg httpProxyConfig
			if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil {
				info.HasHeader = cfg.Header != ""
				info.PeerVM = cfg.PeerVM
				parsedURL, _ := url.Parse(cfg.Target)
				if parsedURL != nil && parsedURL.User != nil {
					info.HasBasicAuth = true
					parsedURL.User = nil
					info.Target = parsedURL.String()
				} else {
					info.Target = cfg.Target
				}
			}
		case "github":
			var cfg githubIntegrationConfig
			if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil {
				info.Repositories = nonNil(cfg.Repositories)
			}
		}
		integrations = append(integrations, info)
	}

	// Fetch team integrations if user is in a team.
	var hasTeam bool
	if team, err := s.GetTeamForUser(r.Context(), userID); err == nil && team != nil {
		hasTeam = true
		teamIntegrations, _ := withRxRes1(s, r.Context(), (*exedb.Queries).ListIntegrationsByTeam, &team.TeamID)
		for _, ig := range teamIntegrations {
			info := jsonIntegrationInfo{
				Name:        ig.Name,
				Type:        ig.Type,
				Attachments: nonNil(ig.GetAttachments()),
				IsTeam:      true,
			}
			switch ig.Type {
			case "http-proxy":
				var cfg httpProxyConfig
				if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil {
					info.HasHeader = cfg.Header != ""
					parsedURL, _ := url.Parse(cfg.Target)
					if parsedURL != nil && parsedURL.User != nil {
						info.HasBasicAuth = true
						parsedURL.User = nil
						info.Target = parsedURL.String()
					} else {
						info.Target = cfg.Target
					}
				}
			case "github":
				var cfg githubIntegrationConfig
				if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil {
					info.Repositories = nonNil(cfg.Repositories)
				}
			}
			integrations = append(integrations, info)
		}
	}

	var ghIntegrations, proxyIntegrations []jsonIntegrationInfo
	for _, ig := range integrations {
		switch ig.Type {
		case "github":
			ghIntegrations = append(ghIntegrations, ig)
		case "http-proxy":
			proxyIntegrations = append(proxyIntegrations, ig)
		}
	}

	_, ghAccountsFull := s.fetchGitHubAccountDisplayInfo(r.Context(), userID)
	ghAccounts := make([]jsonGitHubAccount, 0, len(ghAccountsFull))
	for _, a := range ghAccountsFull {
		ghAccounts = append(ghAccounts, jsonGitHubAccount{
			GitHubLogin:    a.GitHubLogin,
			TargetLogin:    a.TargetLogin,
			InstallationID: a.InstallationID,
		})
	}

	ghEnabled := s.githubApp.Enabled()
	var ghAppSlug string
	if ghEnabled {
		ghAppSlug = s.githubApp.AppSlug
	}

	var hasPushTokens bool
	if n, err := withRxRes1(s, r.Context(), (*exedb.Queries).HasPushTokens, userID); err == nil {
		hasPushTokens = n != 0
	}

	showIntegrations := s.showIntegrationsNav(r.Context(), userID)

	if !showIntegrations {
		writeJSONOK(w, jsonIntegrationsData{
			Integrations:       make([]jsonIntegrationInfo, 0),
			GitHubIntegrations: make([]jsonIntegrationInfo, 0),
			ProxyIntegrations:  make([]jsonIntegrationInfo, 0),
			GitHubAccounts:     make([]jsonGitHubAccount, 0),
			AllTags:            make([]string, 0),
			TagVMs:             map[string][]string{},
			Boxes:              make([]jsonBoxMinimal, 0),
			IntegrationScheme:  s.integrationScheme(),
			BoxHost:            s.env.BoxHost,
		})
		return
	}

	tagVMs := map[string][]string{}
	var profileBoxes []jsonBoxMinimal
	if userBoxes, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxesForUser, userID); err == nil {
		for _, b := range userBoxes {
			profileBoxes = append(profileBoxes, jsonBoxMinimal{Name: b.Name, Status: b.Status})
			for _, t := range b.GetTags() {
				tagVMs[t] = append(tagVMs[t], b.Name)
			}
		}
	}
	var allTags []string
	for t := range tagVMs {
		allTags = append(allTags, t)
	}

	writeJSONOK(w, jsonIntegrationsData{
		Integrations:       nonNil(integrations),
		GitHubIntegrations: nonNil(ghIntegrations),
		ProxyIntegrations:  nonNil(proxyIntegrations),
		GitHubAccounts:     ghAccounts,
		GitHubEnabled:      ghEnabled,
		GitHubAppSlug:      ghAppSlug,
		HasPushTokens:      hasPushTokens,
		AllTags:            nonNil(allTags),
		TagVMs:             tagVMs,
		Boxes:              nonNil(profileBoxes),
		IntegrationScheme:  s.integrationScheme(),
		BoxHost:            s.env.BoxHost,
		HasTeam:            hasTeam,
	})
}

// nonNil returns the slice if non-nil, or an empty slice. This ensures JSON
// output contains [] instead of null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return make([]T, 0)
	}
	return s
}

// handleReceiptsDownload streams a ZIP archive of receipt PDFs for the last 30 days.
func (s *Server) handleReceiptsDownload(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		http.Error(w, "No billing account", http.StatusNotFound)
		return
	}

	since := time.Now().AddDate(0, 0, -30)
	receipts, err := s.billing.ReceiptURLsAfter(ctx, account.ID, since)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to fetch receipts for download", "error", err, "user_id", userID)
		http.Error(w, "Failed to fetch receipts", http.StatusInternalServerError)
		return
	}
	if len(receipts) == 0 {
		http.Error(w, "No receipts in the last 30 days", http.StatusNotFound)
		return
	}

	// Headers must be set before any body write; errors after this point are logged only.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="receipts.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	fetchClient := &http.Client{Timeout: 30 * time.Second}
	for i, receipt := range receipts {
		filename := fmt.Sprintf("receipt-%s-%02d.html", receipt.Created.Format("2006-01-02"), i+1)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, receipt.URL, nil)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to build receipt request", "url", receipt.URL, "error", err)
			continue
		}
		resp, err := fetchClient.Do(req)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to fetch receipt PDF", "url", receipt.URL, "error", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			s.slog().WarnContext(ctx, "receipt PDF returned non-200", "url", receipt.URL, "status", resp.StatusCode)
			continue
		}
		fw, err := zw.Create(filename)
		if err != nil {
			resp.Body.Close()
			continue
		}
		_, _ = io.Copy(fw, resp.Body)
		resp.Body.Close()
	}
}
