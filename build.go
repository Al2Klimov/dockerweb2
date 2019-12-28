package main

import (
	"bytes"
	"fmt"
	"github.com/google/go-github/v28/github"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

func build(config *githubConfig, patterns map[string]*regexp.Regexp) {
	if !resetTempDir() {
		return
	}

	chFramework := make(chan bool, 1)
	chMods := make(chan bool, 1)

	go fetchFramework(config.Framework, chFramework)
	go fetchMods(config.Mods, patterns, chMods)

	<-chFramework
	<-chMods

	// TODO
}

func resetTempDir() bool {
	log.WithFields(log.Fields{"path": tempDir}).Info("Removing temp dir")

	if errRA := os.RemoveAll(tempDir); errRA != nil && !os.IsNotExist(errRA) {
		log.WithFields(log.Fields{"path": tempDir, "error": jsonableError{errRA}}).Warn("Couldn't remove temp dir")
	}

	log.WithFields(log.Fields{"path": tempDir}).Debug("Creating temp dir")

	if errMA := os.MkdirAll(tempDir, 0700); errMA != nil {
		log.WithFields(log.Fields{"path": tempDir, "error": jsonableError{errMA}}).Error("Couldn't create temp dir")
		return false
	}

	return true
}

func fetchFramework(repo string, done chan<- bool) {
	log.WithFields(log.Fields{"path": frameworkPath}).Info("Fetching Icinga Web 2 itself")

	if _, errSt := os.Stat(frameworkPath); errSt != nil {
		if os.IsNotExist(errSt) {
			log.WithFields(log.Fields{"path": frameworkPath}).Debug("Initializing Git repo for Icinga Web 2")

			frameworkGit := mkTemp()
			if frameworkGit == "" {
				done <- false
				return
			}

			defer rmTemp(frameworkGit)

			if _, ok := runCmd(frameworkGit, "git", "init", "--bare"); !ok {
				done <- false
				return
			}

			{
				_, ok := runCmd(
					frameworkGit,
					"git", "remote", "add", "--mirror=fetch", "--",
					"origin", fmt.Sprintf("https://github.com/%s.git", repo),
				)
				if !ok {
					done <- false
					return
				}
			}

			if !rename(frameworkGit, frameworkPath) {
				done <- false
				return
			}
		} else {
			log.WithFields(log.Fields{"path": frameworkPath, "error": jsonableError{errSt}}).Error("Stat error")
			done <- false
			return
		}
	}

	_, ok := runCmd(frameworkPath, "git", "fetch", "origin")
	done <- ok
	return
}

func fetchMods(mods []modConfig, patterns map[string]*regexp.Regexp, done chan<- bool) {
	gh := github.NewClient(nil)
	chOrgs := make(chan organization, len(mods))

	for _, mod := range mods {
		go fetchOrg(gh, mod.Org, chOrgs)
	}

	repos := make(map[string][]string, len(mods))
	ok := true

	for range mods {
		if res := <-chOrgs; res.repos == nil {
			ok = false
		} else {
			repos[res.name] = res.repos
		}
	}

	if !ok {
		done <- false
		return
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

	// TODO
	log.WithFields(log.Fields{"mods": reposOfMods}).Debug()

	done <- ok
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

func mkTemp() string {
	log.WithFields(log.Fields{"path": tempChild}).Trace("Creating temp dir")

	dir, errTD := ioutil.TempDir(tempDir, "")
	if errTD != nil {
		log.WithFields(log.Fields{"path": tempChild, "error": jsonableError{errTD}}).Error("Couldn't create temp dir")
		dir = ""
	}

	return dir
}

func rmTemp(dir string) {
	log.WithFields(log.Fields{"path": dir}).Trace("Removing temp dir")

	if errRA := os.RemoveAll(dir); errRA != nil && !os.IsNotExist(errRA) {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errRA}}).Warn("Couldn't remove temp dir")
	}
}

func runCmd(wd, name string, arg ...string) (stdout []byte, ok bool) {
	cmd := exec.Command(name, arg...)
	var out, err bytes.Buffer

	cmd.Dir = wd
	cmd.Stdout = &out
	cmd.Stderr = &err

	noInterrupt.RLock()

	log.WithFields(log.Fields{"exe": name, "args": arg, "dir": wd}).Debug("Running command")
	errRn := cmd.Run()

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
