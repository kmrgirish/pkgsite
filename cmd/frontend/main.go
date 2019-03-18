// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"

	"golang.org/x/discovery/internal/frontend"
	"golang.org/x/discovery/internal/postgres"
)

var (
	user     = getEnv("GO_DISCOVERY_DATABASE_USER", "postgres")
	password = getEnv("GO_DISCOVERY_DATABASE_PASSWORD", "")
	host     = getEnv("GO_DISCOVERY_DATABASE_HOST", "localhost")
	dbname   = getEnv("GO_DISCOVERY_DATABASE_NAME", "discovery-database")
	dbinfo   = fmt.Sprintf("user=%s host=%s dbname=%s sslmode=disable", user, host, dbname)

	templates  *template.Template
	staticPath = flag.String("static", "content/static", "path to folder containing static files served")
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	flag.Parse()
	templates = template.Must(template.ParseGlob(fmt.Sprintf("%s/html/*.tmpl", *staticPath)))

	db, err := postgres.Open(dbinfo)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(*staticPath))))
	http.HandleFunc("/", frontend.MakeModuleHandlerFunc(db, "overview.tmpl", templates))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}