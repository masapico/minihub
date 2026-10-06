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
}

type Options struct {
	OSNotificationsEnabled *bool
	WorkspaceTitle         string
	LoginMessage           string
	NetworkPathMode        string
}

func Handler(auth Authenticator, options ...Options) http.Handler {
	enabled := true
	settings := Options{WorkspaceTitle: "minihub", LoginMessage: "ユーザーIDとパスワードを入力してください。", NetworkPathMode: "copy", OSNotificationsEnabled: &enabled}
	if len(options) > 0 {
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
	}
	return &handler{
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
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		servePage(w, r, h.indexPage)
		return
	case "/login":
		if _, err := h.auth.Authenticate(r); err == nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		servePage(w, r, h.loginPage)
		return
	case "/login.html":
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.URL.Path == "/schedules" || r.URL.Path == "/schedules/new" || strings.HasPrefix(r.URL.Path, "/schedules/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := h.auth.Authenticate(r); err != nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		servePage(w, r, h.schedulePage)
		return
	}
	h.static.ServeHTTP(w, r)
}
