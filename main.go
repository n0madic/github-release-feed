package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/caarlos0/env"
	"github.com/google/go-github/github"
	"github.com/google/logger"
	"github.com/gorilla/feeds"
	"github.com/gorilla/mux"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"golang.org/x/oauth2"
)

type config struct {
	GitHubToken string   `env:"GITHUB_TOKEN"`
	Stars       uint     `env:"GITHUB_STARS" envDefault:"1"`
	Languages   []string `env:"GITHUB_LANGUAGES" envSeparator:"," envDefault:"go"`
}

var (
	client *github.Client
	mutex  sync.Mutex

	feed             = make(map[string]*feeds.Feed)
	minimalTimestamp = time.Now().AddDate(0, 0, -1)
	md               = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
		),
	)
)

func main() {
	cfg := config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("config: %s", err)
	}

	var tc *http.Client
	if cfg.GitHubToken != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: cfg.GitHubToken},
		)
		tc = oauth2.NewClient(context.Background(), ts)
	}
	client = github.NewClient(tc)

	for _, lang := range cfg.Languages {
		go checkUpdates(strings.ToLower(lang), cfg.Stars)
	}

	r := mux.NewRouter()
	r.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.githubassets.com/favicons/favicon.png", http.StatusMovedPermanently)
	})
	r.HandleFunc("/{language}", handler)
	http.Handle("/", r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	log.Fatalf("http: %s", http.ListenAndServe(":"+port, nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	language := strings.ToLower(vars["language"])
	feedLang, ok := feed[language]
	if !ok {
		http.Error(w, "language "+language+" not found", http.StatusNotFound)
		return
	}
	mutex.Lock()
	xml, err := feedLang.ToRss()
	mutex.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(xml))
	}
}

func checkUpdates(language string, stars uint) {
	logger := log.WithFields(log.Fields{
		"language": language,
	})
	feed[language] = &feeds.Feed{
		Title: fmt.Sprintf("GitHub %s releases feed", strings.Title(language)),
		Link:  &feeds.Link{Href: "https://github.com"},
	}
	for {
		query := fmt.Sprintf("language:%s stars:>%d", language, stars)
		items, err := getUpdates(query)
		if err != nil {
			logger.Error(err.Error())
		} else {
			mutex.Lock()
			for _, item := range items {
				present := false
				for _, feedItem := range feed[language].Items {
					if item.Id == feedItem.Id {
						present = true
						break
					}
				}
				if !present {
					feed[language].Items = append(feed[language].Items, item)
				}
			}

			sort.Slice(feed[language].Items, func(i, j int) bool {
				return feed[language].Items[i].Updated.After(feed[language].Items[j].Updated)
			})

			if len(feed[language].Items) > 10 {
				feed[language].Items = append([]*feeds.Item{}, feed[language].Items[:10]...)
			}

			for i := len(feed[language].Items) - 1; i >= 0; i-- {
				if feed[language].Items[i].Updated.After(minimalTimestamp) &&
					feed[language].Items[i].Updated.After(feed[language].Updated) {
					logger.Infof("%s at %s", feed[language].Items[i].Title, feed[language].Items[i].Updated)
				}
			}

			feed[language].Updated = feed[language].Items[0].Updated
			mutex.Unlock()
		}
		time.Sleep(5 * time.Minute)
	}
}

func getUpdates(query string) ([]*feeds.Item, error) {
	result, _, err := client.Search.Repositories(context.Background(),
		query,
		&github.SearchOptions{
			Sort:        "updated",
			ListOptions: github.ListOptions{PerPage: 50},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("Search.Repositories returned error: %v", err)
	}

	var items []*feeds.Item
	for _, repo := range result.Repositories {
		releases, _, err := client.Repositories.ListReleases(context.Background(),
			*repo.Owner.Login,
			*repo.Name,
			&github.ListOptions{PerPage: 10},
		)
		if err != nil {
			return nil, fmt.Errorf("Repositories.ListReleases returned error: %v", err)
		}

		if len(releases) > 0 {
			for _, release := range releases {
				if !release.GetPrerelease() && !release.GetDraft() {
					body := release.GetBody()
					if body == "" {
						body = release.GetName()
					}
					description := strings.TrimSpace(body)
					var buf bytes.Buffer
					if err := md.Convert(bytes.TrimSpace([]byte(body)), &buf); err != nil {
						logger.Errorf("md convert: %s", err)
					} else {
						description = buf.String()
					}
					if repo.GetDescription() != "" {
						description = repo.GetDescription() + "<br>" + description
					}
					item := &feeds.Item{
						Title:       fmt.Sprintf("%s release %s", repo.GetFullName(), release.GetTagName()),
						Link:        &feeds.Link{Href: release.GetHTMLURL()},
						Id:          release.GetHTMLURL(),
						Description: description,
						Author:      &feeds.Author{Name: release.Author.GetLogin()},
						Updated:     release.GetPublishedAt().Time,
					}
					items = append(items, item)
				}
			}
		}
	}
	return items, nil
}
