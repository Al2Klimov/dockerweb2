package main

import (
	"archive/tar"
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/google/go-github/v28/github"
	"github.com/hashicorp/go-version"
	log "github.com/sirupsen/logrus"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

func build(config *githubConfig) []byte {
	mods := fetchMods(config.Mods)
	if mods == nil {
		return nil
	}

	delete(mods, config.Framework)

	reposByDir := make(map[string]string, 1+len(mods))
	reposByDir[hex.EncodeToString([]byte(config.Framework))] = config.Framework

	for repo := range mods {
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
		rankedMods := make([][]gitRepo, len(config.Mods))

		{
			rankedModsIdx := make(map[string]*[]gitRepo, len(rankedMods))

			for i, mod := range config.Mods {
				rankedModsIdx[mod.User] = &rankedMods[i]
			}

			delete(updated, config.Framework)

			for remote, repo := range updated {
				if repo.modName != "" {
					reposOfOwner := rankedModsIdx[mods[remote][0]]
					*reposOfOwner = append(*reposOfOwner, repo)
				}
			}
		}

		mods := map[string]*gitRepo{}
		for _, rm := range rankedMods {
			for i := range rm {
				if _, ok := mods[rm[i].modName]; !ok {
					mods[rm[i].modName] = &rm[i]
				}
			}
		}

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

type gitRepo struct {
	remote, latestTag, commit, modName string
}

var modName = regexp.MustCompile(`(?m)^Module:\s*(\S+)`)

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

			if _, ok := runCmd("git", "-C", git, "init", "--bare"); !ok {
				res <- gitRepo{}
				return
			}

			if _, ok := runCmd("git", "-C", git, "remote", "add", "--mirror=fetch", "--", "origin", remote); !ok {
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

	if _, ok := runCmd("git", "-C", local, "fetch", "origin"); !ok {
		res <- gitRepo{}
		return
	}

	tags, ok := runCmd("git", "-C", local, "tag")
	if !ok {
		res <- gitRepo{}
		return
	}

	latestTag := "HEAD"
	latestPreTag := ""

	{
		latestFinalTag := ""

		{
			latestFinal := (*version.Version)(nil)
			latestPre := (*version.Version)(nil)

			for _, line := range bytes.Split(tags, []byte{'\n'}) {
				if match := versionTag.FindSubmatch(line); match != nil {
					ver, errNV := version.NewVersion(string(match[1]))
					if errNV != nil {
						log.WithFields(log.Fields{
							"bad_version": string(match[1]), "error": jsonableError{errNV},
						}).Warn("Something is wrong with a version")
						continue
					}

					if ver.Prerelease() == "" {
						if latestFinal == nil || ver.GreaterThan(latestFinal) {
							latestFinal = ver
							latestFinalTag = string(line)
						}
					} else {
						if latestPre == nil || ver.GreaterThan(latestPre) {
							latestPre = ver
							latestPreTag = string(line)
						}
					}
				}
			}
		}

		if latestFinalTag == "" {
			if latestPreTag != "" {
				latestTag = latestPreTag
			}
		} else {
			latestTag = latestFinalTag
		}
	}

	log.WithFields(log.Fields{"remote": remote, "tag": latestTag}).Trace("Got latest tag")

	latestTagCommit, ok := runCmd("git", "-C", local, "log", "-1", "--format=%H", latestTag)
	if !ok {
		if latestTag == "HEAD" {
			res <- gitRepo{remote, latestTag, latestTag, ""}
		} else {
			res <- gitRepo{}
		}

		return
	}

	latestTagCommit = bytes.TrimSpace(latestTagCommit)
	log.WithFields(log.Fields{"remote": remote, "commit": string(latestTagCommit)}).Trace("Got latest tag's commit")

	moduleName, ok := getModName(local, remote, latestTag)
	if !ok {
		res <- gitRepo{}
		return
	}

	if moduleName == "" && latestTag != "HEAD" {
		if latestPreTag != "" && latestPreTag != latestTag {
			moduleName, ok = getModName(local, remote, latestPreTag)
			if !ok {
				res <- gitRepo{}
				return
			}
		}

		if moduleName == "" {
			moduleName, ok = getModName(local, remote, "HEAD")
			if !ok {
				res <- gitRepo{}
				return
			}
		}
	}

	res <- gitRepo{remote, latestTag, string(latestTagCommit), moduleName}
}

func getModName(local, remote, tag string) (name string, ok bool) {
	lsModInfo, ok := runCmd("git", "-C", local, "ls-tree", "--name-only", tag, "module.info")
	if !ok {
		return "", tag == "HEAD"
	}

	if len(lsModInfo) < 1 {
		log.WithFields(log.Fields{"remote": remote, "tag": tag}).Trace("No module.info file found")
		return "", true
	}

	modInfoTar, ok := runCmd("git", "-C", local, "archive", tag, "module.info")
	if !ok {
		return "", false
	}

	var buf bytes.Buffer

	buf.Write(modInfoTar)
	modInfoTar = nil

	tr := tar.NewReader(&buf)

	for {
		th, errTN := tr.Next()

		if errTN != nil {
			log.WithFields(log.Fields{
				"remote": remote, "tag": tag, "error": jsonableError{errTN},
			}).Error("Got bad output from git archive")

			return "", false
		}

		if th.Name == "module.info" {
			var buf bytes.Buffer
			if _, errCp := io.Copy(&buf, tr); errCp != nil {
				log.WithFields(log.Fields{
					"remote": remote, "tag": tag, "error": jsonableError{errCp},
				}).Error("Got bad output from git archive")

				return "", false
			}

			if match := modName.FindSubmatch(buf.Bytes()); match == nil {
				log.WithFields(log.Fields{
					"remote": remote, "tag": tag,
				}).Trace("module.info file doesn't name any module")

				return "", true
			} else {
				moduleName := string(match[1])

				log.WithFields(log.Fields{
					"remote": remote, "tag": tag, "module": moduleName,
				}).Trace("module.info file names a module")

				return moduleName, true
			}
		}
	}
}

func fetchMods(mods []modConfig) map[string][2]string {
	gh := github.NewClient(nil)
	chUsers := make(chan githubUser, len(mods))

	for _, mod := range mods {
		go fetchUser(gh, mod.User, chUsers)
	}

	repos := make(map[string][]string, len(mods))

	{
		ok := true
		for range mods {
			if res := <-chUsers; res.repos == nil {
				ok = false
			} else {
				repos[res.name] = res.repos
			}
		}

		if !ok {
			return nil
		}
	}

	allRepos := map[string][2]string{}

	for user, repos := range repos {
		for _, repo := range repos {
			allRepos[fmt.Sprintf("%s/%s", user, repo)] = [2]string{user, repo}
		}
	}

	return allRepos
}

type githubUser struct {
	name  string
	repos []string
}

func fetchUser(gh *github.Client, user string, res chan<- githubUser) {
	log.WithFields(log.Fields{"user": user}).Info("Fetching repos of GitHub user")

	var names []string

	{
		var opts = github.RepositoryListOptions{
			Visibility:  "public",
			ListOptions: github.ListOptions{PerPage: 100, Page: 1},
		}

		for {
			repos, _, errLR := gh.Repositories.List(background, user, &opts)
			if errLR != nil {
				log.WithFields(log.Fields{
					"user": user, "error": jsonableError{errLR},
				}).Error("Couldn't fetch repos of GitHub user")

				res <- githubUser{}
			}

			for _, repo := range repos {
				names = append(names, *repo.Name)
			}

			if len(repos) < opts.PerPage {
				break
			}

			opts.Page++
		}
	}

	sort.Strings(names)
	res <- githubUser{user, names}
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
