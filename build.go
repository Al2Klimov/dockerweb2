package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/google/go-github/v28/github"
	"github.com/hashicorp/go-version"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

func build(config *githubConfig, patterns map[string]*regexp.Regexp) []byte {
	mods := fetchMods(config.Mods, patterns)
	if mods == nil {
		return nil
	}

	reposByDir := make(map[string]string, 1+len(mods))
	reposByDir[hex.EncodeToString([]byte(config.Framework))] = config.Framework

	for _, repo := range mods {
		reposByDir[hex.EncodeToString([]byte(repo))] = repo
	}

	chUpd := make(chan map[string]gitRepo, 1)
	chRm := make(chan struct{})

	go updateMirrors(reposByDir, chUpd)
	go rmObsolete(reposByDir, chRm)

	defer waitFor(chRm)

	updated := <-chUpd
	if updated == nil {
		return nil
	}

	var buf bytes.Buffer

	{
		framework := updated[config.Framework]
		fmt.Fprintf(
			&buf,
			`#!/bin/sh
set -exo pipefail

rm -rf dockerweb2-temp
git clone --bare '%s' dockerweb2-temp
# %s
git -C dockerweb2-temp archive --prefix=icingaweb2/ %s |tar -x
`,
			framework.remote, framework.latestTag, framework.commit,
		)
	}

	{
		sortedMods := make([]string, 0, len(mods))
		for mod := range mods {
			sortedMods = append(sortedMods, mod)
		}

		sort.Strings(sortedMods)

		for _, mod := range sortedMods {
			repo := updated[mods[mod]]
			fmt.Fprintf(
				&buf,
				`
if [ ! -e 'icingaweb2/modules/%s' ]; then
	rm -rf dockerweb2-temp
	git clone --bare '%s' dockerweb2-temp
	# %s
	git -C dockerweb2-temp archive '--prefix=icingaweb2/modules/%s/' %s |tar -x
fi
`,
				mod, repo.remote, repo.latestTag, mod, repo.commit,
			)
		}
	}

	fmt.Fprint(&buf, `
rm -rf dockerweb2-temp
`)

	return buf.Bytes()
}

type gitRepo struct {
	remote, latestTag, commit string
}

func fetchGit(remote, local string, res chan<- gitRepo) {
	log.WithFields(log.Fields{"remote": remote, "local": local}).Info("Fetching Git repo")

	if _, errSt := os.Stat(local); errSt != nil {
		if os.IsNotExist(errSt) {
			log.WithFields(log.Fields{"local": local}).Debug("Initializing Git repo")

			git := mkTemp()
			if git == "" {
				res <- gitRepo{}
				return
			}

			defer rmDir(git, log.TraceLevel)

			if _, ok := runCmd(git, "git", "init", "--bare"); !ok {
				res <- gitRepo{}
				return
			}

			if _, ok := runCmd(git, "git", "remote", "add", "--mirror=fetch", "--", "origin", remote); !ok {
				res <- gitRepo{}
				return
			}

			if !rename(git, local) {
				res <- gitRepo{}
				return
			}
		} else {
			log.WithFields(log.Fields{"path": local, "error": jsonableError{errSt}}).Error("Stat error")
			res <- gitRepo{}
			return
		}
	}

	if _, ok := runCmd(local, "git", "fetch", "origin"); !ok {
		res <- gitRepo{}
		return
	}

	tags, ok := runCmd(local, "git", "tag")
	if !ok {
		res <- gitRepo{}
		return
	}

	latestTag := "HEAD"

	{
		latestVersion := (*version.Version)(nil)
		for _, line := range bytes.Split(tags, []byte{'\n'}) {
			if match := versionTag.FindSubmatch(line); match != nil {
				ver, errNV := version.NewVersion(string(match[1]))
				if errNV != nil {
					log.WithFields(log.Fields{
						"bad_version": string(match[1]), "error": jsonableError{errNV},
					}).Warn("Something is wrong with a version")
					continue
				}

				if latestVersion == nil || ver.GreaterThan(latestVersion) {
					latestVersion = ver
					latestTag = string(line)
				}
			}
		}
	}

	log.WithFields(log.Fields{"remote": remote, "tag": latestTag}).Trace("Got latest tag")

	latestTagCommit, ok := runCmd(local, "git", "log", "-1", "--format=%H", latestTag)
	if !ok {
		res <- gitRepo{}
		return
	}

	latestTagCommit = bytes.TrimSpace(latestTagCommit)
	log.WithFields(log.Fields{"remote": remote, "commit": string(latestTagCommit)}).Trace("Got latest tag's commit")

	res <- gitRepo{remote, latestTag, string(latestTagCommit)}
}

func fetchMods(mods []modConfig, patterns map[string]*regexp.Regexp) map[string]string {
	gh := github.NewClient(nil)
	chOrgs := make(chan organization, len(mods))

	for _, mod := range mods {
		go fetchOrg(gh, mod.Org, chOrgs)
	}

	repos := make(map[string][]string, len(mods))

	{
		ok := true
		for range mods {
			if res := <-chOrgs; res.repos == nil {
				ok = false
			} else {
				repos[res.name] = res.repos
			}
		}

		if !ok {
			return nil
		}
	}

	reposOfMods := map[string]string{}

	for _, mod := range mods {
		ourRepos := repos[mod.Org]

		for _, repo := range mod.Repos {
			rgx := patterns[repo]

			for _, ourRepo := range ourRepos {
				if match := rgx.FindStringSubmatch(ourRepo); match != nil && strings.TrimSpace(match[1]) != "" {
					if _, ok := reposOfMods[match[1]]; !ok {
						reposOfMods[match[1]] = fmt.Sprintf("%s/%s", mod.Org, ourRepo)
					}
				}
			}
		}
	}

	return reposOfMods
}

type organization struct {
	name  string
	repos []string
}

func fetchOrg(gh *github.Client, org string, res chan<- organization) {
	log.WithFields(log.Fields{"org": org}).Info("Fetching repos of GitHub organization")

	repos, _, errLR := gh.Repositories.ListByOrg(background, org, &publicRepos)
	if errLR != nil {
		log.WithFields(log.Fields{
			"org": org, "error": jsonableError{errLR},
		}).Error("Couldn't fetch repos of GitHub organization")

		res <- organization{}
	}

	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, *repo.Name)
	}

	sort.Strings(names)
	res <- organization{org, names}
}

func updateMirrors(expected map[string]string, res chan<- map[string]gitRepo) {
	if !mkDir(gitMirrorPath) {
		res <- nil
		return
	}

	chGit := make(chan gitRepo, len(expected))
	for dir, repo := range expected {
		go fetchGit(githubPrefix+repo+githubSuffix, path.Join(gitMirrorPath, dir), chGit)
	}

	ok := true
	mirrors := make(map[string]gitRepo, len(expected))

	for range expected {
		if repo := <-chGit; repo == (gitRepo{}) {
			ok = false
		} else {
			mirrors[strings.TrimSuffix(strings.TrimPrefix(repo.remote, githubPrefix), githubSuffix)] = repo
		}
	}

	if !ok {
		mirrors = nil
	}

	res <- mirrors
}

func rmObsolete(expected map[string]string, done chan<- struct{}) {
	defer close(done)

	log.WithFields(log.Fields{"path": gitMirrorPath}).Trace("Listing dir")

	entries, errRD := ioutil.ReadDir(gitMirrorPath)
	if errRD != nil {
		if !os.IsNotExist(errRD) {
			log.WithFields(log.Fields{"path": gitMirrorPath, "error": jsonableError{errRD}}).Error("Couldn't list dir")
		}

		return
	}

	var wg sync.WaitGroup

	for _, entry := range entries {
		name := entry.Name()
		if _, ok := expected[name]; !ok {
			wg.Add(1)
			go rmOne(path.Join(gitMirrorPath, name), &wg)
		}
	}

	wg.Wait()
}

func rmOne(dir string, wg *sync.WaitGroup) {
	defer wg.Done()
	rmDir(dir, log.InfoLevel)
}

func mkTemp() string {
	log.WithFields(log.Fields{"path": tempChild}).Trace("Creating temp dir")

	dir, errTD := ioutil.TempDir(tempDir, "")
	if errTD != nil {
		log.WithFields(log.Fields{"path": tempChild, "error": jsonableError{errTD}}).Error("Couldn't create temp dir")
		dir = ""
	}

	return dir
}
