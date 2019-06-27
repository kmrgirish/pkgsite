// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package frontend

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/discovery/internal/middleware"
	"golang.org/x/discovery/internal/postgres"
)

// Server handles requests for the various frontend pages.
type Server struct {
	http.Handler

	db              *postgres.DB
	templateDir     string
	reloadTemplates bool
	errorPage       []byte

	mu        sync.RWMutex // Protects all fields below
	templates map[string]*template.Template
}

// New creates a new Server for the given database and template directory.
// reloadTemplates should be used during development when it can be helpful to
// reload templates from disk each time a page is loaded.
func NewServer(db *postgres.DB, staticPath string, reloadTemplates bool) (*Server, error) {
	templateDir := filepath.Join(staticPath, "html")
	ts, err := parsePageTemplates(templateDir)
	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %v", err)
	}

	mux := http.NewServeMux()
	s := &Server{
		Handler:         mux,
		db:              db,
		templateDir:     templateDir,
		reloadTemplates: reloadTemplates,
		templates:       ts,
	}
	errorPageBytes, err := s.renderErrorPage(http.StatusInternalServerError, nil)
	if err != nil {
		return nil, fmt.Errorf("s.renderErrorPage(http.StatusInternalServerError, nil): %v", err)
	}
	s.errorPage = errorPageBytes

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticPath))))
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fmt.Sprintf("%s/img/favicon.ico", http.Dir(staticPath)))
	})
	mux.Handle("/pkg/", http.StripPrefix("/pkg", http.HandlerFunc(s.handleDetails)))
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/license-policy", s.handleStaticPage("license_policy.tmpl", "Licenses"))
	mux.HandleFunc("/", s.handleStaticPage("index.tmpl", "Go Discovery"))

	return s, nil
}

// handleStaticPage handles requests to a template that contains no dynamic
// content.
func (s *Server) handleStaticPage(templateName, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce, ok := middleware.GetNonce(r.Context())
		if !ok {
			log.Printf("middleware.GetNonce(r.Context()): nonce was not set")
		}
		s.servePage(w, templateName, basePageData{Title: title, Nonce: nonce})
	}
}

// basePageData contains fields shared by all pages when rendering templates.
type basePageData struct {
	Title string
	Query string
	Nonce string
}

// GoogleAnalyticsTrackingID returns the tracking ID from
// func (b basePageData) GoogleAnalyticsTrackingID() string {
	return "UA-141356704-1"
}

// errorPage contains fields for rendering a HTTP error page.
type errorPage struct {
	basePageData
	Message          string
	SecondaryMessage template.HTML
}

func (s *Server) serveErrorPage(w http.ResponseWriter, r *http.Request, status int, page *errorPage) {
	buf, err := s.renderErrorPage(status, page)
	if err != nil {
		log.Printf("s.renderErrorPage(w, %d, %v): %v", status, page, err)
		buf = s.errorPage
		status = http.StatusInternalServerError
	}

	w.WriteHeader(status)
	if _, err := io.Copy(w, bytes.NewReader(buf)); err != nil {
		log.Printf("Error copying template %q buffer to ResponseWriter: %v", "error.tmpl", err)
	}
}

// renderErrorPage executes error.tmpl with the given errorPage
func (s *Server) renderErrorPage(status int, page *errorPage) ([]byte, error) {
	statusInfo := fmt.Sprintf("%d %s", status, http.StatusText(status))
	if page == nil {
		page = &errorPage{
			Message: statusInfo,
		}
	}
	page.basePageData = basePageData{Title: statusInfo}
	return s.renderPage("error.tmpl", page)
}

// servePage is used to execute all templates for a *Server.
func (s *Server) servePage(w http.ResponseWriter, templateName string, page interface{}) {
	if s.reloadTemplates {
		s.mu.Lock()
		var err error
		s.templates, err = parsePageTemplates(s.templateDir)
		s.mu.Unlock()
		if err != nil {
			log.Printf("Error parsing templates: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	buf, err := s.renderPage(templateName, page)
	if err != nil {
		log.Printf("s.renderPage(%q, %v): %v", templateName, page, err)
		w.WriteHeader(http.StatusInternalServerError)
		buf = s.errorPage
	}
	if _, err := io.Copy(w, bytes.NewReader(buf)); err != nil {
		log.Printf("Error copying template %q buffer to ResponseWriter: %v", templateName, err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// renderErrorPage executes the given templateName with page.
func (s *Server) renderPage(templateName string, page interface{}) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var buf bytes.Buffer
	if err := s.templates[templateName].Execute(&buf, page); err != nil {
		log.Printf("Error executing page template %q: %v", templateName, err)
		return nil, err

	}
	return buf.Bytes(), nil
}

// parsePageTemplates parses html templates contained in the given base
// directory in order to generate a map of Name->*template.Template.
//
// Separate templates are used so that certain contextual functions (e.g.
// templateName) can be bound independently for each page.
func parsePageTemplates(base string) (map[string]*template.Template, error) {
	htmlSets := [][]string{
		{"index.tmpl"},
		{"error.tmpl"},
		{"search.tmpl"},
		{"license_policy.tmpl"},
		{"doc.tmpl", "details.tmpl"},
		{"importedby.tmpl", "details.tmpl"},
		{"imports.tmpl", "details.tmpl"},
		{"licenses.tmpl", "details.tmpl"},
		{"module.tmpl", "details.tmpl"},
		{"overview.tmpl", "details.tmpl"},
		{"versions.tmpl", "details.tmpl"},
	}

	templates := make(map[string]*template.Template)
	for _, set := range htmlSets {
		t, err := template.New("base.tmpl").Funcs(template.FuncMap{
			"add":     func(i, j int) int { return i + j },
			"curYear": func() int { return time.Now().Year() },
			"pluralize": func(i int, s string) string {
				if i == 1 {
					return s
				}
				return s + "s"
			},
		}).ParseFiles(filepath.Join(base, "base.tmpl"))
		if err != nil {
			return nil, fmt.Errorf("ParseFiles: %v", err)
		}
		helperGlob := filepath.Join(base, "helpers", "*.tmpl")
		if _, err := t.ParseGlob(helperGlob); err != nil {
			return nil, fmt.Errorf("ParseGlob(%q): %v", helperGlob, err)
		}

		var files []string
		for _, f := range set {
			files = append(files, filepath.Join(base, "pages", f))
		}
		if _, err := t.ParseFiles(files...); err != nil {
			return nil, fmt.Errorf("ParseFiles(%v): %v", files, err)
		}
		templates[set[0]] = t
	}
	return templates, nil
}