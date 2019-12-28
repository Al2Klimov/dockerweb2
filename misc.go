package main

import (
	"encoding"
	"fmt"
	"github.com/robfig/cron/v3"
	lev "github.com/schollz/closestmatch/levenshtein"
	log "github.com/sirupsen/logrus"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
)

const watchPath = "./"
const configPath = "config.yml"
const frameworkPath = "framework"
const tempDir = "tmp"

var tempChild = path.Join(tempDir, "*")
var noInterrupt sync.RWMutex

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

type githubConfig struct {
	Framework string `yaml:"framework"`
}

type configuration struct {
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Build struct {
		Every string `yaml:"every"`
	} `yaml:"build"`
	GitHub githubConfig `yaml:"github"`
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
