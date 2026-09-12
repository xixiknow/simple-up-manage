package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"simple-up-manage/internal/upstream"
)

func TestNewAPIBalancePrefersTokenUsage(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"object":"billing_subscription","hard_limit_usd":5000000}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"object":"list","total_usage":0}`)
		case "/api/usage/token/", "/api/usage/token":
			_, _ = io.WriteString(w, `{"code":true,"data":{"object":"token_usage","total_available":1000000,"unlimited_quota":false}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{Client: upstream.NewClient()}
	rem, unlimited, source, status, err := s.newAPIBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if unlimited || rem == nil || *rem != 2 {
		t.Fatalf("remaining: %v unlimited=%v", rem, unlimited)
	}
	if source != "api.usage.token" || status != 200 {
		t.Fatalf("source=%s status=%d", source, status)
	}
	for _, p := range paths {
		if p == "/v1/dashboard/billing/subscription" || p == "/v1/dashboard/billing/usage" {
			t.Fatalf("should not call billing when token usage works: %v", paths)
		}
	}
}

func TestNewAPIBalanceFallsBackToBillingUSD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/usage/token/", "/api/usage/token":
			http.NotFound(w, r)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"object":"billing_subscription","hard_limit_usd":12.5}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"object":"list","total_usage":250}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{Client: upstream.NewClient()}
	rem, unlimited, source, _, err := s.newAPIBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if unlimited || rem == nil || *rem != 10 {
		t.Fatalf("remaining: %v unlimited=%v", rem, unlimited)
	}
	if source != "billing.subscription-usage" {
		t.Fatalf("source=%s", source)
	}
}

func TestNewAPIBalanceNormalizesTokenScaleBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/usage/token/", "/api/usage/token":
			http.NotFound(w, r)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"hard_limit_usd":5000000}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"total_usage":100000000}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{Client: upstream.NewClient()}
	rem, unlimited, source, _, err := s.newAPIBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if unlimited || rem == nil || *rem != 8 {
		t.Fatalf("remaining: %v unlimited=%v source=%s", rem, unlimited, source)
	}
}

func TestNewAPIBalanceUnlimitedTokenUsesUserBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/usage/token/", "/api/usage/token":
			_, _ = io.WriteString(w, `{"code":true,"data":{"total_available":0,"unlimited_quota":true}}`)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"hard_limit_usd":20}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"total_usage":500}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{Client: upstream.NewClient()}
	rem, unlimited, source, _, err := s.newAPIBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if unlimited || rem == nil || *rem != 15 {
		t.Fatalf("remaining: %v unlimited=%v source=%s", rem, unlimited, source)
	}
}

func TestNewAPIBalanceUnlimitedWhenBothUnlimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/usage/token/", "/api/usage/token":
			_, _ = io.WriteString(w, `{"data":{"unlimited_quota":true}}`)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"hard_limit_usd":100000000}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{Client: upstream.NewClient()}
	rem, unlimited, source, _, err := s.newAPIBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if rem != nil || !unlimited || source != "api.usage.token" {
		t.Fatalf("remaining: %v unlimited=%v source=%s", rem, unlimited, source)
	}
}
