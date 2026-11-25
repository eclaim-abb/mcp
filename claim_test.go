package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClaimStatusOverMCP(t *testing.T) {
	for _, tt := range []struct {
		name         string
		phone        string
		verifyStatus int
		verifyBody   string
		activity     bool
		timeout      bool
		unauthorized bool
		repeat       int
		wantError    string
		wantVerify   int32
		wantTracking int32
	}{
		{name: "verified", activity: true, wantVerify: 1, wantTracking: 1},
		{name: "no activity", wantVerify: 1, wantTracking: 1},
		{name: "mismatch", verifyStatus: 400, verifyBody: `{"message":"Nomor telepon tidak sesuai."}`, wantError: "VERIFICATION_FAILED", wantVerify: 1},
		{name: "unknown registration", verifyStatus: 400, verifyBody: `{"message":"Data claim not found!"}`, wantError: "VERIFICATION_FAILED", wantVerify: 1},
		{name: "rate limit", repeat: 6, verifyStatus: 400, verifyBody: `{"message":"Nomor telepon tidak sesuai."}`, wantError: "RATE_LIMITED", wantVerify: 5},
		{name: "missing token", verifyBody: `{"data":{"verified":true}}`, wantError: "SERVICE_UNAVAILABLE", wantVerify: 1},
		{name: "timeout", timeout: true, wantError: "SERVICE_UNAVAILABLE", wantVerify: 1},
		{name: "invalid digits", phone: "٠٠٤٢", wantError: "INVALID_INPUT"},
		{name: "invalid service token", unauthorized: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var verifies, tracks atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Errorf("unexpected request method or content type")
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("params_data_client") != "42" {
					t.Errorf("unexpected client form: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case verifyPath:
					verifies.Add(1)
					if r.Form.Get("params_claim_reg_id") != "REG12345" || r.Form.Get("params_phone_last4") != "0042" {
						t.Error("verification did not preserve input")
					}
					if tt.timeout {
						<-r.Context().Done()
						return
					}
					if tt.verifyStatus != 0 {
						w.WriteHeader(tt.verifyStatus)
					}
					if tt.verifyBody != "" {
						_, _ = io.WriteString(w, tt.verifyBody)
						return
					}
					_, _ = io.WriteString(w, `{"data":{"verified":true,"verify_token":"synthetic-token"}}`)
				case trackingPath:
					tracks.Add(1)
					if verifies.Load() == 0 || r.Form.Get("params_data_tracking") != "REG12345" ||
						r.Form.Get("params_tracking_type") != "reg_id" || r.Form.Get("params_verify_token") != "synthetic-token" {
						t.Error("tracking called without the verified registration and token")
					}
					activity := "null"
					if tt.activity {
						activity = `[{"ClaimActivityNo":9,"ClaimStatusName":"Entri riwayat"},{"ClaimActivityNo":7,"ClaimStatusName":"Sedang diperbaiki","ClaimActivityCreatedDate":"20 November 2026 15:10:00","ClaimActivityDetailNotes":"internal"}]`
					}
					_, _ = fmt.Fprintf(w, `{"data":{"client":[{"ClaimRegisterId":"REG12345","ClaimStatus":"PROGRESS","ClaimLastActivityNo":7,"ClaimPICName":"internal"}],"activity":%s}}`, activity)
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()

			timeout := 5 * time.Second
			if tt.timeout {
				timeout = 100 * time.Millisecond
			}
			cfg := config{apiURL: api.URL, clientID: "42", token: strings.Repeat("x", 32), timeout: timeout}
			server := httptest.NewServer(newHandler(cfg))
			defer server.Close()
			resp, err := server.Client().Get(server.URL + "/health")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("health HTTP %d", resp.StatusCode)
			}

			token := cfg.token
			if tt.unauthorized {
				token = "wrong"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
				Endpoint:             server.URL + "/mcp",
				HTTPClient:           &http.Client{Transport: testBearer{token: token}},
				DisableStandaloneSSE: true,
			}, nil)
			if tt.unauthorized {
				if err == nil {
					_ = session.Close()
					t.Fatal("accepted invalid service token")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "get_claim_status" || !tools.Tools[0].Annotations.ReadOnlyHint {
				t.Fatalf("tool discovery: %+v, %v", tools, err)
			}
			phone := tt.phone
			if phone == "" {
				phone = "0042"
			}
			var result *mcp.CallToolResult
			for i := 0; i < max(1, tt.repeat); i++ {
				registration := " reg12345 "
				if i%2 == 1 {
					registration = "REG12345"
				}
				result, err = session.CallTool(ctx, &mcp.CallToolParams{
					Name:      "get_claim_status",
					Arguments: claimInput{RegistrationNumber: registration, PhoneLast4: phone},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if verifies.Load() != tt.wantVerify || tracks.Load() != tt.wantTracking {
				t.Fatalf("verify calls = %d, tracking calls = %d", verifies.Load(), tracks.Load())
			}
			if tt.wantError != "" {
				if !result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != tt.wantError || result.StructuredContent != nil {
					t.Fatalf("want tool error %s, got %+v", tt.wantError, result)
				}
				return
			}
			got, err := json.Marshal(result.StructuredContent, json.Deterministic(true))
			if err != nil || result.IsError {
				t.Fatalf("unexpected tool result %+v: %v", result, err)
			}
			want := `{"registration_number":"REG12345","status":"PROGRESS"}`
			if tt.activity {
				want = `{"activity_label":"Sedang diperbaiki","registration_number":"REG12345","status":"PROGRESS","updated_at":"20 November 2026 15:10:00"}`
			}
			if string(got) != want {
				t.Fatalf("status = %s, want %s", got, want)
			}
		})
	}
}

type testBearer struct{ token string }

func (b testBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestAttemptWindow(t *testing.T) {
	limiter := attemptLimiter{entries: make(map[string][]time.Time)}
	now := time.Now()
	for i := 0; i < attemptLimit; i++ {
		if !limiter.allow("REG12345", now) {
			t.Fatal("rejected a permitted attempt")
		}
	}
	if limiter.allow("REG12345", now.Add(attemptDuration-time.Second)) {
		t.Fatal("allowed more than five attempts")
	}
	if !limiter.allow("REG12345", now.Add(attemptDuration)) {
		t.Fatal("did not allow an attempt after the window elapsed")
	}
}
