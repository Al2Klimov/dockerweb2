//go:generate go run github.com/Al2Klimov/go-gen-source-repos

package main

import (
	"github.com/fsnotify/fsnotify"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"path"
	"regexp"
	"strings"
	"time"
)

func main() {
	initLogging()
	go wait4term()

	log.WithFields(log.Fields{"projects": GithubcomAl2klimovGo_gen_source_repos}).Debug(
		"For the terms of use, the source code and the authors see the projects this program is assembled from",
	)

	watcher := mkWatcher()

LoadConfig:
	for {
		var config *configuration
		var ok bool
		var level log.Level
		var schedule cron.Schedule
		var nextBuild time.Time
		var timer *time.Timer = nil
		var timerCh <-chan time.Time = nil
		patterns := map[string]*regexp.Regexp{}

		{
			if config, ok = loadConfig(); ok {
				if config.Log.Level == "" {
					config.Log.Level = "info"
				}

				{
					var errPL error
					if level, errPL = log.ParseLevel(config.Log.Level); errPL != nil {
						log.WithFields(log.Fields{
							"bad_level": config.Log.Level, "did_you_mean": jsonableBadLogLevelAlt{config.Log.Level},
						}).Error("Bad log level")

						ok = false
					}
				}

				if strings.TrimSpace(config.Build.Every) == "" {
					log.Error("Build schedule missing")
					ok = false
				} else {
					var errCP error
					if schedule, errCP = cronParser.Parse(config.Build.Every); errCP != nil {
						log.WithFields(log.Fields{
							"bad_schedule": config.Build.Every, "error": jsonableError{errCP},
						}).Error("Bad build schedule")
						ok = false
					}
				}

				if strings.TrimSpace(config.GitHub.Framework) == "" {
					log.Error("Icinga Web 2 repository missing")
					ok = false
				}

				for i, mod := range config.GitHub.Mods {
					if strings.TrimSpace(mod.User) == "" {
						log.WithFields(log.Fields{"mods_idx": i}).Error("Organization missing")
						ok = false
					}

					if len(mod.Repos) == 0 {
						log.WithFields(log.Fields{"mods_idx": i}).Error("Repository patterns missing")
						ok = false
					} else {
						for _, repo := range mod.Repos {
							if _, ok := patterns[repo]; !ok {
								if rgx, errRC := regexp.Compile(repo); errRC == nil {
									if rgx.NumSubexp() == 1 {
										patterns[repo] = rgx
									} else {
										log.WithFields(log.Fields{
											"bad_pattern": repo, "subpatterns": rgx.NumSubexp(),
										}).Error("Repository pattern with not exactly one subpattern")

										patterns[repo] = nil
										ok = false
									}
								} else {
									log.WithFields(log.Fields{
										"bad_pattern": repo, "error": jsonableError{errRC},
									}).Error("Bad repository pattern")

									patterns[repo] = nil
									ok = false
								}
							}
						}
					}
				}

				if strings.TrimSpace(config.Deploy.Remote) == "" {
					log.Error("Deploy repository missing")
					ok = false
				}

				if strings.TrimSpace(config.Deploy.Script) == "" {
					log.Error("Deploy path missing")
					ok = false
				}

				if strings.TrimSpace(config.Deploy.Commit) == "" {
					log.Error("Deploy commit message missing")
					ok = false
				}
			}
		}

		if ok {
			log.WithFields(log.Fields{"old": log.GetLevel(), "new": level}).Trace("Changing log level")
			log.SetLevel(level)

			now := time.Now()
			nextBuild = schedule.Next(now)

			log.WithFields(log.Fields{"next_build": nextBuild}).Info("Scheduling next build")
			timer, timerCh = prepareSleep(nextBuild.Sub(now))
		}

		for {
			select {
			case now := <-timerCh:
				if now.Before(nextBuild) {
					timer, timerCh = prepareSleep(nextBuild.Sub(now))
				} else {
					rmDir(tempDir, log.InfoLevel)
					if mkDir(tempDir) {
						log.Info("Building")
						if script := build(&config.GitHub, patterns); script != nil {
							log.Info("Deploying")
							deploy(&config.Deploy, script)
						}
					}

					nextBuild = schedule.Next(time.Now())

					log.WithFields(log.Fields{"next_build": nextBuild}).Info("Scheduling next build")
					timer, timerCh = prepareSleep(nextBuild.Sub(now))
				}
			case event := <-watcher.Events:
				log.WithFields(log.Fields{
					"parent": watchPath, "child": event.Name, "op": jsonableStringer{event.Op},
				}).Trace("Got FS event")

				if event.Op&^fsnotify.Chmod != 0 && path.Clean(event.Name) == configPath {
					if timer != nil {
						timer.Stop()
					}

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

func loadConfig() (config *configuration, ok bool) {
	log.WithFields(log.Fields{"path": configPath}).Info("Loading config")

	raw, errRF := ioutil.ReadFile(configPath)
	if errRF != nil {
		log.WithFields(log.Fields{"path": configPath, "error": jsonableError{errRF}}).Error("Couldn't read config")
		return
	}

	config = &configuration{}
	if errYU := yaml.Unmarshal(raw, config); errYU != nil {
		log.WithFields(log.Fields{"path": configPath, "error": jsonableError{errYU}}).Error("Couldn't parse config")
		return
	}

	ok = true
	return
}

func prepareSleep(duration time.Duration) (*time.Timer, <-chan time.Time) {
	log.WithFields(log.Fields{"ns": duration}).Trace("Sleeping")

	timer := time.NewTimer(duration)
	return timer, timer.C
}
