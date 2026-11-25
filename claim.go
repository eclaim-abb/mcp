package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	verifyPath              = "/api/v2/claim-status/verify-phone"
	trackingPath            = "/api/v2/claim-status/tracking"
	attemptLimit            = 5
	attemptDuration         = 15 * time.Minute
	maxTrackedRegistrations = 10_000
	maxAPIResponseBytes     = 1 << 20
)

var (
	registrationPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9/-]{0,21}$`)
	phoneLast4Pattern   = regexp.MustCompile(`^[0-9]{4}$`)
	errInvalidInput     = errors.New("INVALID_INPUT")
	errVerification     = errors.New("VERIFICATION_FAILED")
	errRateLimit        = errors.New("RATE_LIMITED")
	errUnavailable      = errors.New("SERVICE_UNAVAILABLE")
)

type claimInput struct {
	RegistrationNumber string `json:"registration_number" jsonschema:"Nomor registrasi klaim kendaraan"`
	PhoneLast4         string `json:"phone_last4" jsonschema:"Tepat empat digit terakhir nomor HP yang terdaftar pada klaim"`
}

type claimStatus struct {
	RegistrationNumber string `json:"registration_number"`
	Status             string `json:"status"`
	ActivityLabel      string `json:"activity_label,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

type claimChecker struct {
	apiURL, clientID string
	timeout          time.Duration
	client           *http.Client
	attempts         attemptLimiter
}

func (c *claimChecker) check(ctx context.Context, _ *mcp.CallToolRequest, input claimInput) (*mcp.CallToolResult, claimStatus, error) {
	registration := strings.ToUpper(strings.TrimSpace(input.RegistrationNumber))
	if !registrationPattern.MatchString(registration) || !phoneLast4Pattern.MatchString(input.PhoneLast4) {
		return nil, claimStatus{}, errInvalidInput
	}
	if !c.attempts.allow(registration, time.Now()) {
		return nil, claimStatus{}, errRateLimit
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var verified struct {
		Data struct {
			Verified bool   `json:"verified"`
			Token    string `json:"verify_token"`
		} `json:"data"`
	}
	err := c.postForm(ctx, verifyPath, url.Values{
		"params_data_client":  {c.clientID},
		"params_claim_reg_id": {registration},
		"params_phone_last4":  {input.PhoneLast4},
	}, &verified)
	if err != nil {
		return nil, claimStatus{}, err
	}
	if !verified.Data.Verified || strings.TrimSpace(verified.Data.Token) == "" {
		return nil, claimStatus{}, errUnavailable
	}

	var tracking struct {
		Data struct {
			Client []struct {
				ClaimRegisterId     string
				ClaimStatus         string
				ClaimLastActivityNo uint
			} `json:"client"`
			Activity []struct {
				ClaimActivityNo          uint
				ClaimStatusName          string
				ClaimActivityCreatedDate string
			} `json:"activity"`
		} `json:"data"`
	}
	err = c.postForm(ctx, trackingPath, url.Values{
		"params_data_client":   {c.clientID},
		"params_data_tracking": {registration},
		"params_tracking_type": {"reg_id"},
		"params_verify_token":  {verified.Data.Token},
	}, &tracking)
	if err != nil {
		return nil, claimStatus{}, err
	}
	for _, claim := range tracking.Data.Client {
		if !strings.EqualFold(claim.ClaimRegisterId, registration) || strings.TrimSpace(claim.ClaimStatus) == "" {
			continue
		}
		result := claimStatus{RegistrationNumber: claim.ClaimRegisterId, Status: claim.ClaimStatus}
		for _, activity := range tracking.Data.Activity {
			if claim.ClaimLastActivityNo != 0 && activity.ClaimActivityNo == claim.ClaimLastActivityNo {
				result.ActivityLabel = activity.ClaimStatusName
				result.UpdatedAt = activity.ClaimActivityCreatedDate
				break
			}
		}
		return nil, result, nil
	}
	return nil, claimStatus{}, errUnavailable
}

func (c *claimChecker) postForm(ctx context.Context, path string, form url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return errUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("eclaim-api %s: request failed", path)
		return errUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return errRateLimit
	}
	if path == verifyPath && resp.StatusCode == http.StatusBadRequest {
		return errVerification
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("eclaim-api %s: HTTP %d", path, resp.StatusCode)
		return errUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil || len(raw) > maxAPIResponseBytes || json.Unmarshal(raw, result) != nil {
		log.Printf("eclaim-api %s: invalid response", path)
		return errUnavailable
	}
	return nil
}

type attemptLimiter struct {
	mu          sync.Mutex
	entries     map[string][]time.Time
	nextCleanup time.Time
}

func (l *attemptLimiter) allow(registration string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-attemptDuration)
	if !now.Before(l.nextCleanup) {
		for key, attempts := range l.entries {
			if !attempts[len(attempts)-1].After(cutoff) {
				delete(l.entries, key)
			}
		}
		l.nextCleanup = now.Add(time.Minute)
	}
	attempts, exists := l.entries[registration]
	if !exists && len(l.entries) >= maxTrackedRegistrations {
		return false
	}
	for len(attempts) > 0 && !attempts[0].After(cutoff) {
		attempts = attempts[1:]
	}
	if len(attempts) >= attemptLimit {
		return false
	}
	l.entries[registration] = append(attempts, now)
	return true
}
