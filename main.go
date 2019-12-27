package main

import (
	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

func main() {
	initLogging()
	go wait4term()

	watcher := mkWatcher()

LoadConfig:
	for {
		if config, ok := loadConfig(); ok {
			if config.Log.Level == "" {
				config.Log.Level = "info"
			}

			if level, errPL := log.ParseLevel(config.Log.Level); errPL == nil {
				log.WithFields(log.Fields{"old": log.GetLevel(), "new": level}).Trace("Changing log level")
				log.SetLevel(level)
			} else {
				log.WithFields(log.Fields{
					"bad_level": config.Log.Level, "did_you_mean": jsonableBadLogLevelAlt{config.Log.Level},
				}).Error("Bad log level")
			}
		}

		for {
			select {
			case event := <-watcher.Events:
				log.WithFields(log.Fields{
					"parent": watchPath, "child": event.Name, "op": jsonableStringer{event.Op},
				}).Trace("Got FS event")

				if event.Op&^fsnotify.Chmod != 0 && event.Name == configPath {
					continue LoadConfig
				}
			case errWa := <-watcher.Errors:
				log.WithFields(log.Fields{"error": jsonableError{errWa}}).Fatal("FS watcher error")
			}
		}
	}
}

func mkWatcher() *fsnotify.Watcher {
	log.Trace("Setting up FS watcher")

	watcher, errNW := fsnotify.NewWatcher()
	if errNW != nil {
		log.WithFields(log.Fields{"error": jsonableError{errNW}}).Fatal("Couldn't set up FS watcher")
	}

	log.WithFields(log.Fields{"path": watchPath}).Debug("Watching FS")

	if errWA := watcher.Add(watchPath); errWA != nil {
		log.WithFields(log.Fields{"path": watchPath, "error": jsonableError{errWA}}).Fatal("Couldn't watch FS")
	}

	return watcher
}

func loadConfig() (config configuration, ok bool) {
	log.WithFields(log.Fields{"path": configPath}).Info("Loading config")

	raw, errRF := ioutil.ReadFile(configPath)
	if errRF != nil {
		log.WithFields(log.Fields{"path": configPath, "error": jsonableError{errRF}}).Error("Couldn't read config")
		return
	}

	if errYU := yaml.Unmarshal(raw, &config); errYU != nil {
		log.WithFields(log.Fields{"path": configPath, "error": jsonableError{errYU}}).Error("Couldn't parse config")
		return
	}

	ok = true
	return
}
