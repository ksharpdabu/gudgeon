package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/gops/agent"
	"github.com/pkg/profile"
	log "github.com/sirupsen/logrus"

	"github.com/chrisruffalo/gudgeon/config"
	"github.com/chrisruffalo/gudgeon/engine"
	"github.com/chrisruffalo/gudgeon/provider"
	"github.com/chrisruffalo/gudgeon/util"
	"github.com/chrisruffalo/gudgeon/version"
	"github.com/chrisruffalo/gudgeon/web"
)

// default divider
var divider = "==============================="

// Gudgeon Core Gudgeon object for executing a Gudgeon process
type Gudgeon struct {
	config   *config.GudgeonConfig
	engine   engine.Engine
	provider provider.Provider
	web      web.Web
}

// NewGudgeon Create a new Gudgeon instance from a given Gudgeon Config
func NewGudgeon(config *config.GudgeonConfig) *Gudgeon {
	return &Gudgeon{
		config: config,
	}
}

func (gudgeon *Gudgeon) Start() error {
	// get config
	config := gudgeon.config

	// error
	var err error

	// create engine which handles resolution, logging, etc
	engine, err := engine.NewEngine(config)
	if err != nil {
		return err
	}
	if engine == nil {
		return fmt.Errorf("Could not create required engine component")
	}
	gudgeon.engine = engine

	// create a new provider and start hosting
	provider := provider.NewProvider(engine)
	provider.Host(config, engine)
	gudgeon.provider = provider

	// open web ui if web enabled
	if config.Web.Enabled {
		web := web.New()
		web.Serve(config, engine)
		gudgeon.web = web
	}

	// try and print out error if we caught one during startup
	if recovery := recover(); recovery != nil {
		return fmt.Errorf("unrecoverable error: %s", recovery)
	}

	return nil
}

func (gudgeon *Gudgeon) Shutdown() {
	// stop providers
	if gudgeon.provider != nil {
		log.Infof("Shutting down DNS endpoints...")
		gudgeon.provider.Shutdown()
	}

	// stop web
	if gudgeon.web != nil {
		log.Infof("Shutting down Web service...")
		gudgeon.web.Stop()
	}

	// stop/shutdown engine
	log.Infof("Shutting down Engine...")
	gudgeon.engine.Shutdown()
}

func main() {
	// set initial log instance configuration
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// load command options
	opts, err := config.Options(version.GetLongVersion())
	if err != nil {
		log.Errorf("%s", err)
		os.Exit(1)
	}

	// debug print config
	log.Info(divider)
	log.Infof("Gudgeon %s", version.GetLongVersion())
	log.Info(divider)

	// start profiling if enabled
	if opts.DebugOptions.Profile {
		log.Info("Starting profiling...")
		// start profile
		defer profile.Start().Stop()
		// start agent
		err := agent.Listen(agent.Options{})
		if err != nil {
			log.Errorf("Could not starting GOPS profilling agent: %s", err)
		}
	}

	// load config
	filename := string(opts.AppOptions.ConfigPath)
	config, warnings, err := config.Load(filename)
	if err != nil {
		log.Errorf("%s", err)
		os.Exit(1)
	}

	// configure log file from configuration if additional configuration is available

	// print log warnings and continue
	if len(warnings) > 0 {
		for _, warn := range warnings {
			log.Warn(warn)
		}
	}

	// print log file information
	if "" != filename {
		log.Infof("Configuration file: %s", filename)
	}

	// create new Gudgeon instance
	instance := NewGudgeon(config)

	// start new instance
	err = instance.Start()
	if err != nil {
		log.Errorf("Error starting Gudgeon: %s", err)
		os.Exit(1)
	}

	// wait for signal
	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig

	// clean out session directory
	if "" != config.SessionRoot() {
		util.ClearDirectory(config.SessionRoot())
	}

	log.Infof("Signal (%s) received, stopping", s)
	// stop gudgeon, hopefully gracefully
	instance.Shutdown()

}
