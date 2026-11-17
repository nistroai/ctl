package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRejectsOffOriginRedirectsBeforeSendingCredentials(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		login  bool
	}{
		{name: "authenticated GET", status: http.StatusFound},
		{name: "login POST 307", status: http.StatusTemporaryRedirect, login: true},
		{name: "login POST 308", status: http.StatusPermanentRedirect, login: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				fmt.Fprint(w, `{"token":"unexpected"}`)
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-token" {
					t.Error("initial request did not contain the test token")
				}
				http.Redirect(w, r, target.URL+"/capture", test.status)
			}))
			defer source.Close()
			client := NewClient(source.URL, "private-token")
			var err error
			if test.login {
				_, err = client.SignIn(t.Context(), SignInRequest{Email: "user@example.com", Password: "private-password"})
			} else {
				_, err = client.Resource(t.Context(), http.MethodGet, "/deployments", nil)
			}
			if err == nil || !strings.Contains(err.Error(), "different origin") {
				t.Fatalf("expected off-origin rejection, got %v", err)
			}
			if received.Load() != 0 {
				t.Fatal("redirect destination received a request containing credentials")
			}
		})
	}
}

func TestClientRejectsHTTPSDowngrade(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := NewClient(source.URL, "private-token")
	client.httpClient.Transport = source.Client().Transport
	_, err := client.SignIn(t.Context(), SignInRequest{Email: "user@example.com", Password: "private-password"})
	if err == nil || !strings.Contains(err.Error(), "different origin") {
		t.Fatalf("expected downgrade rejection, got %v", err)
	}
	if received.Load() != 0 {
		t.Fatal("plaintext destination received the redirected login")
	}
}

func TestClientFollowsSameOriginRedirect(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/signin" {
			http.Redirect(w, r, "/signin-canonical", http.StatusTemporaryRedirect)
			return
		}
		received.Add(1)
		var body SignInRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/signin-canonical" || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "Bearer private-token" || body.Password != "private-password" {
			t.Error("same-origin redirect did not preserve the authenticated POST")
		}
		fmt.Fprint(w, `{"token":"session-token"}`)
	}))
	defer server.Close()
	response, err := NewClient(server.URL, "private-token").SignIn(t.Context(), SignInRequest{Email: "user@example.com", Password: "private-password"})
	if err != nil || response.Token != "session-token" || received.Load() != 1 {
		t.Fatalf("same-origin login redirect failed: %v", err)
	}
}

func TestClientBoundsSameOriginRedirects(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "private-token").Resource(t.Context(), http.MethodGet, "/loop", nil)
	if err == nil || !strings.Contains(err.Error(), "10 redirects") || received.Load() != 10 {
		t.Fatalf("redirect loop was not bounded: requests=%d error=%v", received.Load(), err)
	}
}

func TestClientTimeoutAndCallerCancellation(t *testing.T) {
	for _, test := range []struct {
		name           string
		requestTimeout time.Duration
		callerTimeout  time.Duration
	}{
		{name: "configured request deadline", requestTimeout: 20 * time.Millisecond, callerTimeout: time.Second},
		{name: "earlier caller deadline", requestTimeout: time.Second, callerTimeout: 20 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), test.callerTimeout)
			defer cancel()
			client := NewClient(server.URL, "private-token", WithTimeout(test.requestTimeout))
			_, err := client.Resource(ctx, http.MethodGet, "/slow", nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected request deadline, got %v", err)
			}
		})
	}
}

func TestClientRejectsInvalidTimeoutBeforeRequest(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
	}))
	defer server.Close()
	for _, timeout := range []time.Duration{0, -time.Second} {
		_, err := NewClient(server.URL, "private-token", WithTimeout(timeout)).Resource(t.Context(), http.MethodGet, "/deployments", nil)
		if err == nil {
			t.Fatalf("invalid timeout %s was accepted", timeout)
		}
	}
	if received.Load() != 0 {
		t.Fatal("invalid timeout sent a request")
	}
}
