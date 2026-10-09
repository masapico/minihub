package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

//go:embed index.html login.html schedule.html favicon.ico favicon.svg css js vendor
var files embed.FS

// Files returns the embedded file system containing vendor assets.
func Files() embed.FS {
	return files
}

var pages = template.Must(template.ParseFS(files, "index.html", "login.html", "schedule.html"))

type Authenticator interface {
	Authenticate(*http.Request) (string, error)
}

type handler struct {
	auth         Authenticator
	static       http.Handler
	indexPage    []byte
	loginPage    []byte
	schedulePage []byte
	basePath     string
	ssoMode      bool
}

type Options struct {
	BasePath               string
	OSNotificationsEnabled *bool
	WorkspaceTitle         string
	LoginMessage           string
	NetworkPathMode        string
	SSOMode                bool
	MiniAuthURL            string
}

func Handler(auth Authenticator, options ...Options) http.Handler {
	enabled := true
	settings := Options{WorkspaceTitle: "minihub", LoginMessage: "ユーザーIDとパスワードを入力してください。", NetworkPathMode: "copy", OSNotificationsEnabled: &enabled}
	if len(options) > 0 {
		settings.BasePath = options[0].BasePath
		if options[0].OSNotificationsEnabled != nil {
			settings.OSNotificationsEnabled = options[0].OSNotificationsEnabled
		}
		if options[0].WorkspaceTitle != "" {
			settings.WorkspaceTitle = options[0].WorkspaceTitle
		}
		if options[0].LoginMessage != "" {
			settings.LoginMessage = options[0].LoginMessage
		}
		if options[0].NetworkPathMode != "" {
			settings.NetworkPathMode = options[0].NetworkPathMode
		}
		settings.SSOMode = options[0].SSOMode
		settings.MiniAuthURL = options[0].MiniAuthURL
	}
	return &handler{
		basePath:     settings.BasePath,
		ssoMode:      settings.SSOMode,
		auth:         auth,
		static:       http.FileServer(http.FS(files)),
		indexPage:    renderPage("index.html", settings),
		loginPage:    renderPage("login.html", settings),
		schedulePage: renderPage("schedule.html", settings),
	}
}

func renderPage(name string, data Options) []byte {
	var page bytes.Buffer
	if err := pages.ExecuteTemplate(&page, name, data); err != nil {
		panic(err)
	}
	return page.Bytes()
}

func servePage(w http.ResponseWriter, r *http.Request, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodGet {
		_, _ = w.Write(page)
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/login.html") && r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/":
		if _, err := h.auth.Authenticate(r); err != nil {
			if h.ssoMode {
				loginURL := h.basePath + "/api/auth/sso/login"
				if r.URL.RawQuery != "" {
					loginURL += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, loginURL, http.StatusFound)
				return
			}
			http.Redirect(w, r, h.basePath+"/login", http.StatusFound)
			return
		}
		servePage(w, r, h.indexPage)
		return
	case "/login":
		if _, err := h.auth.Authenticate(r); err == nil {
			http.Redirect(w, r, h.basePath+"/", http.StatusFound)
			return
		}
		if h.ssoMode {
			next := r.URL.Query().Get("next")
			loginURL := h.basePath + "/api/auth/sso/login"
			if next != "" {
				loginURL += "?next=" + url.QueryEscape(next)
			}
			http.Redirect(w, r, loginURL, http.StatusFound)
			return
		}
		servePage(w, r, h.loginPage)
		return
	case "/login.html":
		http.Redirect(w, r, h.basePath+"/login", http.StatusFound)
		return
	}
	if r.URL.Path == "/schedules" || r.URL.Path == "/schedules/new" || strings.HasPrefix(r.URL.Path, "/schedules/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := h.auth.Authenticate(r); err != nil {
			loginTarget := h.basePath + "/login?next=" + url.QueryEscape(h.basePath+r.URL.RequestURI())
			if h.ssoMode {
				loginTarget = h.basePath + "/api/auth/sso/login?next=" + url.QueryEscape(h.basePath+r.URL.RequestURI())
			}
			http.Redirect(w, r, loginTarget, http.StatusFound)
			return
		}
		servePage(w, r, h.schedulePage)
		return
	}
	h.static.ServeHTTP(w, r)
}

// Mount serves the complete application, including API and WebSocket handlers,
// below a validated, normalized base path. The proxy must preserve this path.
func Mount(basePath string, next http.Handler) http.Handler {
	if basePath == "" {
		return next
	}
	stripped := http.StripPrefix(basePath, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == basePath {
			target := basePath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, basePath+"/") {
			http.NotFound(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	})
}
