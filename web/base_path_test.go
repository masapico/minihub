package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestDeploymentPaths(t *testing.T) {
	for _, base := range []string{"", "/hub", "/apps/team-chat"} {
		t.Run(base, func(t *testing.T) {
			app := Mount(base, Handler(testAuthenticator(true), Options{BasePath: base}))
			guest := Mount(base, Handler(testAuthenticator(false), Options{BasePath: base}))
			for _, path := range []string{"/", "/schedules", "/schedules/new", "/schedules/example"} {
				response := request(t, app, base+path)
				if response.Code != http.StatusOK {
					t.Fatalf("GET %s: %d", base+path, response.Code)
				}
				checkPagePaths(t, app, response.Body.String(), base)
			}
			checkPagePaths(t, guest, request(t, guest, base+"/login").Body.String(), base)
			for _, tc := range []struct{ path, location string }{
				{"/", base + "/login"},
				{"/login.html", base + "/login"},
				{"/schedules/example?tab=answers", base + "/login?next=" + url.QueryEscape(base+"/schedules/example?tab=answers")},
			} {
				response := request(t, guest, base+tc.path)
				if response.Code != http.StatusFound || response.Header().Get("Location") != tc.location {
					t.Fatalf("GET %s: status=%d location=%q", tc.path, response.Code, response.Header().Get("Location"))
				}
			}
			if response := request(t, app, base+"/login"); response.Header().Get("Location") != base+"/" {
				t.Fatalf("authenticated login location=%q", response.Header().Get("Location"))
			}
			if base != "" {
				response := request(t, app, base+"?channel=general")
				if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != base+"/?channel=general" {
					t.Fatalf("canonical root status=%d location=%q", response.Code, response.Header().Get("Location"))
				}
				for _, path := range []string{"/", "/login", "/api/me", base + "other/", base + "other/api/me"} {
					if response := request(t, app, path); response.Code != http.StatusNotFound {
						t.Fatalf("outside path %s: status=%d", path, response.Code)
					}
				}
			}
		})
	}
}

// Follow every local stylesheet, script and SVG icon reference from the actual
// rendered pages so a missed prefix or missing helper fails the test.
func checkPagePaths(t *testing.T, app http.Handler, page, base string) {
	t.Helper()
	if !strings.Contains(page, `data-base-path="`+base+`"`) {
		t.Fatal("base path missing from HTML")
	}
	if i := strings.Index(page, base+"/js/urls.js"); i < 0 || i > strings.Index(page, base+"/vendor/bootstrap/bootstrap.bundle.min.js") {
		t.Fatal("URL helper must load before application scripts")
	}
	for _, match := range regexp.MustCompile(`(?:href|src)="(/[^"\s]*)"`).FindAllStringSubmatch(page, -1) {
		target, err := url.Parse(match[1])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(target.Path, base+"/") {
			t.Fatalf("unprefixed reference: %s", target)
		}
		if response := request(t, app, target.RequestURI()); response.Code != http.StatusOK {
			t.Fatalf("page reference %s: status=%d", target, response.Code)
		}
	}
}

func TestMountedAPIKeepsAuthenticationAndRequestBody(t *testing.T) {
	var seen bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/channels/team/messages" || r.URL.Query().Get("after") != "42" || r.Host != "myhost.com" || r.Header.Get("X-CSRF-Token") != "token" {
			t.Errorf("request changed: %s %s host=%s", r.Method, r.URL, r.Host)
		}
		body, err := io.ReadAll(r.Body)
		cookie, cookieErr := r.Cookie("minihub_session")
		if err != nil || string(body) != `{"text":"hello"}` || cookieErr != nil || cookie.Value != "session" {
			t.Error("request body or session cookie changed")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "https://myhost.com/hub/api/channels/team/messages?after=42", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("X-CSRF-Token", "token")
	req.AddCookie(&http.Cookie{Name: "minihub_session", Value: "session"})
	recorder := httptest.NewRecorder()
	Mount("/hub", next).ServeHTTP(recorder, req)
	if !seen || recorder.Code != http.StatusNoContent {
		t.Fatalf("mounted request did not reach handler: %d", recorder.Code)
	}
}
