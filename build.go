package main

import (
	"bytes"
	"fmt"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"os/exec"
)

func build(config githubConfig) {
	if !resetTempDir() {
		return
	}

	if !fetchFramework(config.Framework) {
		return
	}

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

func fetchFramework(repo string) bool {
	log.WithFields(log.Fields{"path": frameworkPath}).Info("Fetching Icinga Web 2 itself")

	if _, errSt := os.Stat(frameworkPath); errSt != nil {
		if os.IsNotExist(errSt) {
			log.WithFields(log.Fields{"path": frameworkPath}).Debug("Initializing Git repo for Icinga Web 2")

			frameworkGit := mkTemp()
			if frameworkGit == "" {
				return false
			}

			defer rmTemp(frameworkGit)

			if _, ok := runCmd(frameworkGit, "git", "init", "--bare"); !ok {
				return false
			}

			{
				_, ok := runCmd(
					frameworkGit,
					"git", "remote", "add", "--mirror=fetch", "--",
					"origin", fmt.Sprintf("https://github.com/%s.git", repo),
				)
				if !ok {
					return false
				}
			}

			if !rename(frameworkGit, frameworkPath) {
				return false
			}
		} else {
			log.WithFields(log.Fields{"path": frameworkPath, "error": jsonableError{errSt}}).Error("Stat error")
			return false
		}
	}

	_, ok := runCmd(frameworkPath, "git", "fetch", "origin")
	return ok
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
