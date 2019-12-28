package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/google/go-github/v28/github"
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

func build(config *githubConfig, patterns map[string]*regexp.Regexp) {
	rmDir(tempDir, log.InfoLevel)
	if !mkDir(tempDir) {
		return
	}

	chFramework := make(chan bool, 1)
	chMods := make(chan bool, 1)

	go fetchGit(fmt.Sprintf("https://github.com/%s.git", config.Framework), frameworkPath, chFramework)
	go fetchMods(config.Mods, patterns, chMods)

	<-chFramework
	<-chMods

	// TODO
}

func mkDir(dir string) bool {
	log.WithFields(log.Fields{"path": dir}).Debug("Creating dir")

	if errMA := os.MkdirAll(dir, 0700); errMA != nil {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errMA}}).Error("Couldn't create dir")
		return false
	}

	return true
}

func fetchGit(remote, local string, done chan<- bool) {
	log.WithFields(log.Fields{"remote": remote, "local": local}).Info("Fetching Git repo")

	if _, errSt := os.Stat(local); errSt != nil {
		if os.IsNotExist(errSt) {
			log.WithFields(log.Fields{"local": local}).Debug("Initializing Git repo")

			git := mkTemp()
			if git == "" {
				done <- false
				return
			}

			defer rmDir(git, log.TraceLevel)

			if _, ok := runCmd(git, "git", "init", "--bare"); !ok {
				done <- false
				return
			}

			if _, ok := runCmd(git, "git", "remote", "add", "--mirror=fetch", "--", "origin", remote); !ok {
				done <- false
				return
			}

			if !rename(git, local) {
				done <- false
				return
			}
		} else {
			log.WithFields(log.Fields{"path": local, "error": jsonableError{errSt}}).Error("Stat error")
			done <- false
			return
		}
	}

	_, ok := runCmd(local, "git", "fetch", "origin")
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
			done <- false
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
			done <- false
			return
		}
	}

	chUpd := make(chan bool, 1)
	chRm := make(chan struct{})

	go updateMods(modGits, chUpd)
	go rmObsolete(modGits, entries, chRm)

	<-chRm
	done <- <-chUpd
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

func updateMods(expected map[string][2]string, done chan<- bool) {
	if !mkDir(modsPath) {
		done <- false
		return
	}

	chGit := make(chan bool, len(expected))
	for _, repo := range expected {
		go fetchGit(
			fmt.Sprintf("https://github.com/%s/%s.git", repo[0], repo[1]),
			path.Join(modsPath, fmt.Sprintf(
				"%s_%s", hex.EncodeToString([]byte(repo[0])), hex.EncodeToString([]byte(repo[1])),
			)),
			chGit,
		)
	}

	ok := true
	for range expected {
		if !<-chGit {
			ok = false
		}
	}

	done <- ok
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
