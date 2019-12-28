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
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

func build(config *githubConfig, patterns map[string]*regexp.Regexp) []byte {
	rmDir(tempDir, log.InfoLevel)
	if !mkDir(tempDir) {
		return nil
	}

	chFramework := make(chan gitRepo, 1)
	chMods := make(chan map[string]gitRepo, 1)

	go fetchGit(fmt.Sprintf("https://github.com/%s.git", config.Framework), frameworkPath, chFramework)
	go fetchMods(config.Mods, patterns, chMods)

	framework := <-chFramework
	mods := <-chMods

	if framework == (gitRepo{}) || mods == nil {
		return nil
	}

	var buf bytes.Buffer

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

	{
		sortedMods := make([]string, 0, len(mods))
		for mod := range mods {
			sortedMods = append(sortedMods, mod)
		}

		sort.Strings(sortedMods)

		for _, mod := range sortedMods {
			repo := mods[mod]

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

func mkDir(dir string) bool {
	log.WithFields(log.Fields{"path": dir}).Debug("Creating dir")

	if errMA := os.MkdirAll(dir, 0700); errMA != nil {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errMA}}).Error("Couldn't create dir")
		return false
	}

	return true
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

func fetchMods(mods []modConfig, patterns map[string]*regexp.Regexp, res chan<- map[string]gitRepo) {
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
			res <- nil
			return
		}
	}

	reposOfMods := map[string][2]string{}

	for _, mod := range mods {
		ourRepos := repos[mod.Org]

		for _, repo := range mod.Repos {
			rgx := patterns[repo]

			for _, ourRepo := range ourRepos {
				if match := rgx.FindStringSubmatch(ourRepo); match != nil && strings.TrimSpace(match[1]) != "" {
					if _, ok := reposOfMods[match[1]]; !ok {
						reposOfMods[match[1]] = [2]string{mod.Org, ourRepo}
					}
				}
			}
		}
	}

	modGits := map[string][2]string{}
	for _, repo := range reposOfMods {
		modGits[fmt.Sprintf(
			"%s_%s", hex.EncodeToString([]byte(repo[0])), hex.EncodeToString([]byte(repo[1])),
		)] = repo
	}

	log.WithFields(log.Fields{"path": modsPath}).Trace("Listing dir")

	entries, errRD := ioutil.ReadDir(modsPath)
	if errRD != nil {
		if os.IsNotExist(errRD) {
			entries = nil
		} else {
			log.WithFields(log.Fields{"path": modsPath, "error": jsonableError{errRD}}).Error("Couldn't list dir")
			res <- nil
			return
		}
	}

	chUpd := make(chan map[string]gitRepo, 1)
	chRm := make(chan struct{})

	go updateMods(modGits, chUpd)
	go rmObsolete(modGits, entries, chRm)

	updated := <-chUpd

	byRepo := make(map[[2]string]gitRepo, len(updated))
	for dir, repo := range updated {
		byRepo[modGits[dir]] = repo
	}

	byName := make(map[string]gitRepo, len(byRepo))
	for mod, repo := range reposOfMods {
		byName[mod] = byRepo[repo]
	}

	<-chRm
	res <- byName
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

func updateMods(expected map[string][2]string, res chan<- map[string]gitRepo) {
	if !mkDir(modsPath) {
		res <- nil
		return
	}

	remotes := make(map[string]string, len(expected))
	chGit := make(chan gitRepo, len(expected))

	for mod, repo := range expected {
		remote := fmt.Sprintf("https://github.com/%s/%s.git", repo[0], repo[1])
		remotes[remote] = mod

		go fetchGit(
			remote,
			path.Join(modsPath, fmt.Sprintf(
				"%s_%s", hex.EncodeToString([]byte(repo[0])), hex.EncodeToString([]byte(repo[1])),
			)),
			chGit,
		)
	}

	ok := true
	mods := make(map[string]gitRepo, len(expected))

	for range expected {
		if repo := <-chGit; repo == (gitRepo{}) {
			ok = false
		} else {
			mods[remotes[repo.remote]] = repo
		}
	}

	if !ok {
		mods = nil
	}

	res <- mods
}

func rmObsolete(expected map[string][2]string, actual []os.FileInfo, done chan<- struct{}) {
	defer close(done)

	var wg sync.WaitGroup

	for _, entry := range actual {
		name := entry.Name()
		if _, ok := expected[name]; !ok {
			wg.Add(1)
			go rmOne(path.Join(modsPath, name), &wg)
		}
	}

	wg.Wait()
}

func rmOne(dir string, wg *sync.WaitGroup) {
	defer wg.Done()
	rmDir(dir, log.InfoLevel)
}

func rmDir(dir string, logLevel log.Level) {
	log.WithFields(log.Fields{"path": dir}).Log(logLevel, "Removing dir")

	if errRA := os.RemoveAll(dir); errRA != nil {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errRA}}).Warn("Couldn't remove dir")
	}
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

func runCmd(wd, name string, arg ...string) (stdout []byte, ok bool) {
	cmd := exec.Command(name, arg...)
	var out, err bytes.Buffer

	cmd.Dir = wd
	cmd.Stdout = &out
	cmd.Stderr = &err

	noInterrupt.RLock()
	execSemaphore.Acquire(background, 1)

	log.WithFields(log.Fields{"exe": name, "args": arg, "dir": wd}).Debug("Running command")
	errRn := cmd.Run()

	execSemaphore.Release(1)
	noInterrupt.RUnlock()

	if errRn != nil {
		log.WithFields(log.Fields{
			"exe": name, "args": arg, "dir": wd, "error": jsonableError{errRn},
			"stdout": jsonableStringer{&out}, "stderr": jsonableStringer{&err},
		}).Error("Command failed")

		return nil, false
	}

	return out.Bytes(), true
}

func rename(old, new string) bool {
	log.WithFields(log.Fields{"old": old, "new": new}).Trace("Renaming")

	if errRn := os.Rename(old, new); errRn != nil {
		log.WithFields(log.Fields{"old": old, "new": new, "error": jsonableError{errRn}}).Error("Couldn't rename")
		return false
	}

	return true
}
