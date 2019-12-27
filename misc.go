package main

import (
	"encoding"
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"os/signal"
	"syscall"
)

const watchPath = "./"
const configPath = "config.yml"

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

type configuration struct {
}

func initLogging() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.TraceLevel)
}

func wait4term() {
	signals := [2]os.Signal{syscall.SIGTERM, syscall.SIGINT}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals[:]...)

	log.WithFields(log.Fields{"signals": signals}).Trace("Listening for signals")

	log.WithFields(log.Fields{"signal": <-ch}).Warn("Terminating")
	os.Exit(0)
}
