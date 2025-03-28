// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/ops-agent/apps"
	"github.com/GoogleCloudPlatform/ops-agent/confgenerator"
	"github.com/GoogleCloudPlatform/ops-agent/internal/healthchecks"
	"github.com/GoogleCloudPlatform/ops-agent/internal/logs"
	"github.com/GoogleCloudPlatform/ops-agent/internal/self_metrics"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	EngineEventID uint32 = 1
	StdoutEventID uint32 = 2
)

func containsString(all []string, s string) bool {
	for _, t := range all {
		if t == s {
			return true
		}
	}
	return false
}

type service struct {
	log          debug.Log
	userConf     string
	outDirectory string
}

func (s *service) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	allArgs := append([]string{}, os.Args[1:]...)
	allArgs = append(allArgs, args[1:]...)
	if err := s.parseFlags(allArgs); err != nil {
		s.log.Error(EngineEventID, fmt.Sprintf("failed to parse arguments: %v", err))
		// ERROR_INVALID_ARGUMENT
		return false, 0x00000057
	}

	if err := s.generateConfigs(ctx); err != nil {
		s.log.Error(EngineEventID, fmt.Sprintf("failed to generate config files: %v", err))
		// 2 is "file not found"
		return false, 2
	}
	s.log.Info(EngineEventID, "generated configuration files")
	s.runHealthChecks()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	if err := s.startSubagents(); err != nil {
		s.log.Error(EngineEventID, fmt.Sprintf("failed to start subagents: %v", err))
		// TODO: Ignore failures for partial startup?
	}
	s.log.Info(EngineEventID, "started subagents")
	defer func() {
		changes <- svc.Status{State: svc.StopPending}
	}()
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				return
			default:
				s.log.Error(EngineEventID, fmt.Sprintf("unexpected control request #%d", c))
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *service) parseFlags(args []string) error {
	s.log.Info(EngineEventID, fmt.Sprintf("args: %#v", args))
	var fs flag.FlagSet
	fs.StringVar(&s.userConf, "in", "", "path to the user specified agent config")
	fs.StringVar(&s.outDirectory, "out", "", "directory to write generated configuration files to")
	return fs.Parse(args)
}

func (s *service) checkForStandaloneAgents(unified *confgenerator.UnifiedConfig) error {
	mgr, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %s", err)
	}
	defer mgr.Disconnect()
	services, err := mgr.ListServices()
	if err != nil {
		return fmt.Errorf("failed to list services: %s", err)
	}

	var errors string
	if unified.HasLogging() && containsString(services, "StackdriverLogging") {
		errors += "We detected an existing Windows service for the StackdriverLogging agent, " +
			"which is not compatible with the Ops Agent when the Ops Agent configuration has a non-empty logging section. " +
			"Please either remove the logging section from the Ops Agent configuration, " +
			"or disable the StackdriverLogging agent, and then retry enabling the Ops Agent. "
	}
	if unified.HasMetrics() && containsString(services, "StackdriverMonitoring") {
		errors += "We detected an existing Windows service for the StackdriverMonitoring agent, " +
			"which is not compatible with the Ops Agent when the Ops Agent configuration has a non-empty metrics section. " +
			"Please either remove the metrics section from the Ops Agent configuration, " +
			"or disable the StackdriverMonitoring agent, and then retry enabling the Ops Agent. "
	}
	if errors != "" {
		return fmt.Errorf("conflicts with existing agents: %s", errors)
	}
	return nil
}

func getHealthCheckResults() []healthchecks.HealthCheckResult {
	logsDir := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, "log")
	gceHealthChecks := healthchecks.HealthCheckRegistryFactory()
	logger := healthchecks.CreateHealthChecksLogger(logsDir)

	return gceHealthChecks.RunAllHealthChecks(logger)
}

func (srv *service) runHealthChecks() {
	healthCheckResults := getHealthCheckResults()
	logger := logs.WindowsServiceLogger{EventID: EngineEventID, Logger: srv.log}
	healthchecks.LogHealthCheckResults(healthCheckResults, logger)
	srv.log.Info(EngineEventID, "Startup checks finished")
}

func (s *service) generateConfigs(ctx context.Context) error {
	// TODO(lingshi) Move this to a shared place across Linux and Windows.
	uc, err := confgenerator.MergeConfFiles(ctx, s.userConf, apps.BuiltInConfStructs)
	if err != nil {
		return err
	}

	s.log.Info(EngineEventID, fmt.Sprintf("Built-in config:\n%s\n", apps.BuiltInConfStructs["windows"]))
	s.log.Info(EngineEventID, fmt.Sprintf("Merged config:\n%s\n", uc))
	if err := s.checkForStandaloneAgents(uc); err != nil {
		return err
	}
	// TODO: Add flag for passing in log/run path?
	logsDir := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, "log")
	stateDir := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, "run")
	for _, subagent := range []string{
		"otel",
		"fluentbit",
	} {
		outDir := filepath.Join(s.outDirectory, subagent)
		if subagent == "otel" {
			// The generated otlp metric json files are used only by the otel service.
			if err = self_metrics.GenerateOpsAgentSelfMetricsOTLPJSON(ctx, s.userConf, outDir); err != nil {
				return err
			}
		}
		if err := uc.GenerateFilesFromConfig(ctx, subagent, logsDir, stateDir, outDir); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) startSubagents() error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	for _, svc := range services[1:] {
		handle, err := manager.OpenService(svc.name)
		if err != nil {
			// service not found?
			return err
		}
		defer handle.Close()
		if err := handle.Start(); err != nil {
			// TODO: Should we be ignoring failures for partial startup?
			if !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
				s.log.Error(EngineEventID, fmt.Sprintf("failed to start %q: %v", svc.name, err))
			}
		}
	}
	return nil
}

type eventLogWriter struct {
	EventID  uint32
	EventLog *eventlog.Log
}

func (w *eventLogWriter) Write(p []byte) (int, error) {
	err := w.EventLog.Info(w.EventID, string(p))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func run(name string) error {
	elog, err := eventlog.Open(name)
	if err != nil {
		// probably futile
		return err
	}
	defer elog.Close()

	// Redirect stdout to the event log to capture internal messages
	log.SetOutput(&eventLogWriter{
		EventID:  StdoutEventID,
		EventLog: elog,
	})

	elog.Info(1, fmt.Sprintf("starting %s service", name))
	err = svc.Run(name, &service{log: elog})
	if err != nil {
		elog.Error(EngineEventID, fmt.Sprintf("%s service failed: %v", name, err))
		return err
	}
	elog.Info(EngineEventID, fmt.Sprintf("%s service stopped", name))
	return nil
}
