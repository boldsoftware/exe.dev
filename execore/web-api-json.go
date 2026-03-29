package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/entitlement"
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

type jsonDashboardData struct {
	User              jsonUserInfo    `json:"user"`
	Boxes             []jsonBoxInfo   `json:"boxes"`
	SharedBoxes       []jsonSharedBox `json:"sharedBoxes"`
	TeamBoxes         []jsonTeamBox   `json:"teamBoxes"`
	InviteCount       int64           `json:"inviteCount"`
	CanRequestInvites bool            `json:"canRequestInvites"`
	SSHCommand        string          `json:"sshCommand"`
	ReplHost          string          `json:"replHost"`
	ShowIntegrations  bool            `json:"showIntegrations"`
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
	PublicKey   string  `json:"publicKey"`
	Comment     string  `json:"comment"`
	Fingerprint string  `json:"fingerprint"`
	AddedAt     *string `json:"addedAt"`
	LastUsedAt  *string `json:"lastUsedAt"`
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

type jsonCreditInfo struct {
	PlanName                string            `json:"planName"`
	SelfServeBilling        bool              `json:"selfServeBilling"`
	PaidPlan                bool              `json:"paidPlan"`
	SkipBilling             bool              `json:"skipBilling"`
	BillingStatus           string            `json:"billingStatus"`
	ShelleyCreditsAvailable float64           `json:"shelleyCreditsAvailable"`
	ShelleyCreditsMax       float64           `json:"shelleyCreditsMax"`
	ExtraCreditsUSD         float64           `json:"extraCreditsUSD"`
	TotalCreditsUSD         float64           `json:"totalCreditsUSD"`
	TotalRemainingPct       float64           `json:"totalRemainingPct"`
	MonthlyAvailableUSD     float64           `json:"monthlyAvailableUSD"`
	MonthlyUsedUSD          float64           `json:"monthlyUsedUSD"`
	MonthlyUsedPct          float64           `json:"monthlyUsedPct"`
	UsedCreditsUSD          float64           `json:"usedCreditsUSD"`
	TotalCapacityUSD        float64           `json:"totalCapacityUSD"`
	UsedBarPct              float64           `json:"usedBarPct"`
	HasShelleyFreeCreditPct bool              `json:"hasShelleyFreeCreditPct"`
	MonthlyCreditsResetAt   string            `json:"monthlyCreditsResetAt"`
	LedgerBalanceUSD        float64           `json:"ledgerBalanceUSD"`
	Purchases               []jsonPurchaseRow `json:"purchases"`
	Gifts                   []jsonGiftRow     `json:"gifts"`
}

type jsonPurchaseRow struct {
	Amount     string `json:"amount"`
	Date       string `json:"date"`
	ReceiptURL string `json:"receiptURL"`
}

type jsonGiftRow struct {
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

type jsonProfileData struct {
	User               jsonUserInfo        `json:"user"`
	SSHKeys            []jsonSSHKey        `json:"sshKeys"`
	Passkeys           []jsonPasskey       `json:"passkeys"`
	SiteSessions       []jsonSiteSession   `json:"siteSessions"`
	SharedBoxes        []jsonSharedBox     `json:"sharedBoxes"`
	TeamInfo           *jsonTeamInfo       `json:"teamInfo"`
	PendingTeamInvites []jsonPendingInvite `json:"pendingTeamInvites"`
	CanEnableTeam      bool                `json:"canEnableTeam"`
	Credits            jsonCreditInfo      `json:"credits"`
	BasicUser          bool                `json:"basicUser"`
	ShowIntegrations   bool                `json:"showIntegrations"`
	InviteCount        int64               `json:"inviteCount"`
	CanRequestInvites  bool                `json:"canRequestInvites"`
}

type jsonIntegrationInfo struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Target       string   `json:"target"`
	HasHeader    bool     `json:"hasHeader"`
	HasBasicAuth bool     `json:"hasBasicAuth"`
	Repositories []string `json:"repositories"`
	Attachments  []string `json:"attachments"`
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
	Boxes              []jsonBoxMinimal      `json:"boxes"`
	IntegrationScheme  string                `json:"integrationScheme"`
	BoxHost            string                `json:"boxHost"`
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

	// Team boxes
	teamBoxResults, _ := s.ListTeamBoxesForAdmin(r.Context(), user.UserID)
	teamBoxes := make([]jsonTeamBox, 0, len(teamBoxResults))
	for _, result := range teamBoxResults {
		teamBoxes = append(teamBoxes, jsonTeamBox{
			Name:         result.Name,
			CreatorEmail: result.CreatorEmail,
			Status:       result.Status,
			ProxyURL:     s.boxProxyAddress(result.Name),
			SSHCommand:   s.boxSSHConnectionCommand(result.Name),
			DisplayTags:  nonNil(parseTags(result.Tags)),
		})
	}

	inviteCount, _ := withRxRes1(s, r.Context(), (*exedb.Queries).CountUnusedInviteCodesForUser, &user.UserID)
	canRequestInvites := s.UserHasEntitlement(r.Context(), entitlement.SourceWeb, entitlement.InviteRequest, userID)
	showIntegrations := s.showIntegrationsNav(r.Context(), userID)

	writeJSONOK(w, jsonDashboardData{
		User:              newJSONUserInfo(user),
		Boxes:             boxes,
		SharedBoxes:       sharedBoxes,
		TeamBoxes:         teamBoxes,
		InviteCount:       inviteCount,
		CanRequestInvites: canRequestInvites,
		SSHCommand:        s.replSSHConnectionCommand(),
		ReplHost:          s.env.ReplHost,
		ShowIntegrations:  showIntegrations,
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
				PublicKey:   dbKey.PublicKey,
				Comment:     dbKey.Comment,
				Fingerprint: dbKey.Fingerprint,
				AddedAt:     formatTimePtr(dbKey.AddedAt),
				LastUsedAt:  formatTimePtr(dbKey.LastUsedAt),
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
		version := entitlement.BasePlan(planRow.PlanID)
		planName = entitlement.PlanName(version)
		selfServeBilling = version == entitlement.CategoryIndividual
		paidPlan = entitlement.PlanIsPaid(version)
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
		balance, err := s.billing.SpendCredits(r.Context(), account.ID, 0, tender.Zero())
		if err == nil {
			creditBalance = balance
		}

		cutoff := time.Now().AddDate(0, 0, -30)
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
		bonusGrantAmount = 0
		bonusRemaining = 0
		shelleyCreditsAvailable = max(shelleyCreditsAvailable-100, 0)
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
		jsonGifts[i] = jsonGiftRow{Amount: g.Amount, Reason: g.Reason}
	}

	showIntegrations := s.showIntegrationsNav(r.Context(), userID)
	inviteCount, _ := withRxRes1(s, r.Context(), (*exedb.Queries).CountUnusedInviteCodesForUser, &user.UserID)
	canRequestInvites := s.UserHasEntitlement(r.Context(), entitlement.SourceWeb, entitlement.InviteRequest, userID)

	profile := jsonProfileData{
		User:               newJSONUserInfoWithNewsletter(user),
		SSHKeys:            nonNil(sshKeys),
		Passkeys:           nonNil(passkeys),
		SiteSessions:       nonNil(siteSessions),
		SharedBoxes:        sharedBoxes,
		PendingTeamInvites: make([]jsonPendingInvite, 0),
		BasicUser:          basicUser,
		ShowIntegrations:   showIntegrations,
		InviteCount:        inviteCount,
		CanRequestInvites:  canRequestInvites,
		Credits: jsonCreditInfo{
			PlanName:                planName,
			SelfServeBilling:        selfServeBilling,
			PaidPlan:                paidPlan,
			SkipBilling:             skipBilling,
			BillingStatus:           billingStatus,
			ShelleyCreditsAvailable: shelleyCreditsAvailable,
			ShelleyCreditsMax:       shelleyCreditsMax,
			ExtraCreditsUSD:         extraCreditsUSD,
			TotalCreditsUSD:         max(shelleyCreditsAvailable+extraCreditsUSD+giftCreditsUSD, 0),
			TotalRemainingPct:       bar.totalRemainingPct,
			MonthlyAvailableUSD:     bar.monthlyAvailable,
			MonthlyUsedUSD:          max(shelleyCreditsMax-bar.monthlyAvailable, 0),
			MonthlyUsedPct:          monthlyUsedPct(bar.monthlyAvailable, shelleyCreditsMax),
			UsedCreditsUSD:          bar.usedCreditsUSD,
			TotalCapacityUSD:        bar.totalCapacity,
			UsedBarPct:              bar.usedBarPct,
			HasShelleyFreeCreditPct: hasShelleyFreeCreditPct,
			MonthlyCreditsResetAt:   nextUTCMonthStart().Format("15:04 on Jan 2"),
			LedgerBalanceUSD:        max(float64(creditBalance.Microcents())/1_000_000, 0),
			Purchases:               nonNil(purchases),
			Gifts:                   nonNil(jsonGifts),
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
				if t, err := time.Parse("2006-01-02 15:04:05", m.JoinedAt); err == nil {
					s := t.Format(time.RFC3339)
					mdi.JoinedAt = &s
				}
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
		if !basicUser && s.UserHasEntitlement(r.Context(), entitlement.SourceWeb, entitlement.TeamCreate, userID) {
			profile.CanEnableTeam = true
		}
	}

	writeJSONOK(w, profile)
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
			Boxes:              make([]jsonBoxMinimal, 0),
			IntegrationScheme:  s.integrationScheme(),
			BoxHost:            s.env.BoxHost,
		})
		return
	}

	tagSet := map[string]bool{}
	var profileBoxes []jsonBoxMinimal
	if userBoxes, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxesForUser, userID); err == nil {
		for _, b := range userBoxes {
			profileBoxes = append(profileBoxes, jsonBoxMinimal{Name: b.Name, Status: b.Status})
			for _, t := range b.GetTags() {
				tagSet[t] = true
			}
		}
	}
	var allTags []string
	for t := range tagSet {
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
		Boxes:              nonNil(profileBoxes),
		IntegrationScheme:  s.integrationScheme(),
		BoxHost:            s.env.BoxHost,
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
