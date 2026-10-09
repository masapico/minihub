package authweb

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"

	"github.com/masapico/minihub/web"
)

//go:embed index.html login.html admin.html
var files embed.FS

var pages = template.Must(template.ParseFS(files, "index.html", "login.html", "admin.html"))

type Authenticator interface {
	Authenticate(*http.Request) (string, error)
}

type Options struct {
	BasePath       string
	WorkspaceTitle string
	LoginMessage   string
}

type Handler struct {
	auth      Authenticator
	basePath  string
	static    http.Handler
	indexPage []byte
	loginPage []byte
	adminPage []byte
}

func NewHandler(auth Authenticator, opt Options) *Handler {
	return &Handler{
		auth:      auth,
		basePath:  opt.BasePath,
		static:    http.FileServer(http.FS(web.Files())),
		indexPage: renderPage("index.html", opt),
		loginPage: renderPage("login.html", opt),
		adminPage: renderPage("admin.html", opt),
	}
}

func renderPage(name string, data Options) []byte {
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, data); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("Cache-Control", "no-cache")

	switch path {
	case "/", "/index.html":
		if _, err := h.auth.Authenticate(r); err != nil {
			http.Redirect(w, r, h.basePath+"/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(h.indexPage)
		return
	case "/login", "/login.html":
		if _, err := h.auth.Authenticate(r); err == nil {
			next := r.URL.Query().Get("next")
			if next != "" {
				http.Redirect(w, r, next, http.StatusFound)
				return
			}
			http.Redirect(w, r, h.basePath+"/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(h.loginPage)
		return
	case "/admin", "/admin.html":
		if _, err := h.auth.Authenticate(r); err != nil {
			http.Redirect(w, r, h.basePath+"/login?next="+url.QueryEscape(h.basePath+"/admin"), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(h.adminPage)
		return
	default:
		h.static.ServeHTTP(w, r)
	}
}

