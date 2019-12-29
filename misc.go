package main

import (
	"bytes"
	"context"
	"encoding"
	"fmt"
	"github.com/google/go-github/v28/github"
	"github.com/robfig/cron/v3"
	lev "github.com/schollz/closestmatch/levenshtein"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

const watchPath = "./"
const configPath = "config.yml"
const gitMirrorPath = "mirrors"
const deployGitPath = "deploy"
const tempDir = "tmp"
const githubPrefix = "https://github.com/"
const githubSuffix = ".git"

var tempChild = path.Join(tempDir, "*")
var noInterrupt sync.RWMutex
var background = context.Background()
var publicRepos = github.RepositoryListByOrgOptions{Type: "public"}
var execSemaphore = semaphore.NewWeighted(int64(runtime.GOMAXPROCS(0)) * 2)
var versionTag = regexp.MustCompile(`\Av?(\d+(?:\.\d+)*)\z`)

var logLevels = func() *lev.ClosestMatch {
	asStrs := make([]string, 0, len(log.AllLevels))
	for _, lvl := range log.AllLevels {
		asStrs = append(asStrs, lvl.String())
	}

	return lev.New(asStrs)
}()

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type jsonableError struct {
	err error
}

var _ encoding.TextMarshaler = jsonableError{}

func (je jsonableError) MarshalText() (text []byte, err error) {
	return []byte(je.err.Error()), nil
}

type jsonableStringer struct {
	str fmt.Stringer
}

var _ encoding.TextMarshaler = jsonableStringer{}

func (js jsonableStringer) MarshalText() (text []byte, err error) {
	return []byte(js.str.String()), nil
}

type jsonableBadLogLevelAlt struct {
	badLogLevel string
}

var _ encoding.TextMarshaler = jsonableBadLogLevelAlt{}

func (jblla jsonableBadLogLevelAlt) MarshalText() (text []byte, err error) {
	return []byte(logLevels.Closest(strings.ToLower(jblla.badLogLevel))), nil
}

type modConfig struct {
	Org   string   `yaml:"org"`
	Repos []string `yaml:"repos"`
}

type githubConfig struct {
	Framework string      `yaml:"framework"`
	Mods      []modConfig `yaml:"mods"`
}

type deployConfig struct {
	Remote string            `yaml:"remote"`
	Config map[string]string `yaml:"config"`
	Script string            `yaml:"script"`
	Commit string            `yaml:"commit"`
}

type configuration struct {
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Build struct {
		Every string `yaml:"every"`
	} `yaml:"build"`
	GitHub githubConfig `yaml:"github"`
	Deploy deployConfig `yaml:"deploy"`
}

func initLogging() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.TraceLevel)
	log.StandardLogger().ExitFunc = exit
}

func wait4term() {
	signals := [2]os.Signal{syscall.SIGTERM, syscall.SIGINT}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals[:]...)

	log.WithFields(log.Fields{"signals": signals}).Trace("Listening for signals")

	log.WithFields(log.Fields{"signal": <-ch}).Warn("Terminating")
	exit(0)
}

func exit(code int) {
	log.Debug("Waiting for all uninterruptable operations to finish")
	noInterrupt.Lock()

	os.Exit(code)
}

func mkDir(dir string) bool {
	log.WithFields(log.Fields{"path": dir}).Debug("Creating dir")

	if errMA := os.MkdirAll(dir, 0700); errMA != nil {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errMA}}).Error("Couldn't create dir")
		return false
	}

	return true
}

func rmDir(dir string, logLevel log.Level) {
	log.WithFields(log.Fields{"path": dir}).Log(logLevel, "Removing dir")

	if errRA := os.RemoveAll(dir); errRA != nil {
		log.WithFields(log.Fields{"path": dir, "error": jsonableError{errRA}}).Warn("Couldn't remove dir")
	}
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

func waitFor(ch <-chan struct{}) {
	<-ch
}
