package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"path"
)

func deploy(config *deployConfig, script []byte) {
	gitConfig := make([]string, 0, len(config.Config)*2)
	for k, v := range config.Config {
		gitConfig = append(gitConfig, "-c", fmt.Sprintf("%s=%s", k, v))
	}

	log.WithFields(log.Fields{"remote": config.Remote, "local": deployGitPath}).Info("Pulling Git repo")

	if _, errSt := os.Stat(deployGitPath); errSt != nil {
		if os.IsNotExist(errSt) {
			log.WithFields(log.Fields{"local": deployGitPath}).Debug("Cloning Git repo")

			git := mkTemp()
			if git == "" {
				return
			}

			defer rmDir(git, log.TraceLevel)

			if _, ok := runCmd("", "git", append(gitConfig, "clone", "--", config.Remote, git)...); !ok {
				return
			}

			if !rename(git, deployGitPath) {
				return
			}
		} else {
			log.WithFields(log.Fields{"path": deployGitPath, "error": jsonableError{errSt}}).Error("Stat error")
			return
		}
	}

	{
		_, ok := runCmd(deployGitPath, "git", append(gitConfig, "remote", "set-url", "--", "origin", config.Remote)...)
		if !ok {
			return
		}
	}

	if _, ok := runCmd(deployGitPath, "git", append(gitConfig, "reset", "--hard")...); !ok {
		return
	}

	if _, ok := runCmd(deployGitPath, "git", append(gitConfig, "pull", "--rebase")...); !ok {
		return
	}

	if !writeFile(path.Join(deployGitPath, config.Script), script) {
		return
	}

	if _, ok := runCmd(deployGitPath, "git", append(gitConfig, "add", "--", config.Script)...); !ok {
		return
	}

	if status, ok := runCmd(deployGitPath, "git", append(gitConfig, "status", "-s")...); ok {
		if len(status) > 0 {
			if _, ok := runCmd(deployGitPath, "git", append(gitConfig, "commit", "-m", config.Commit)...); !ok {
				return
			}
		}
	} else {
		return
	}

	runCmd(deployGitPath, "git", append(gitConfig, "push")...)
}

func writeFile(path string, content []byte) bool {
	log.WithFields(log.Fields{"file": path}).Trace("Writing file")

	if errWF := ioutil.WriteFile(path, content, 0755); errWF != nil {
		log.WithFields(log.Fields{"file": path, "error": jsonableError{errWF}}).Error("Couldn't write file")
		return false
	}

	return true
}
