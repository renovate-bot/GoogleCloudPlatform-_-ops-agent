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
	"fmt"
	"syscall"
	"time"

	"github.com/hashicorp/go-multierror"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

func escapeExe(exepath string, args []string) string {
	// from https://github.com/golang/sys/blob/22da62e12c0c/windows/svc/mgr/mgr.go#L123
	s := syscall.EscapeArg(exepath)
	for _, v := range args {
		s += " " + syscall.EscapeArg(v)
	}
	return s
}

func uninstallDiagnosticService(m *mgr.Mgr) error {
	diagnosticsServiceHandle, err := m.OpenService("google-cloud-ops-agent-diagnostics")
	// err == nil means the service exists.
	// If err != nil, the service does not exist, so nothing to delete.
	if err == nil {
		defer diagnosticsServiceHandle.Close()
		if err := stopService(diagnosticsServiceHandle, 30*time.Second); err != nil {
			return fmt.Errorf("failed to stop the diagnostics Windows service: %w", err)
		}
		if err := diagnosticsServiceHandle.Delete(); err != nil {
			return fmt.Errorf("fails to delete the diagnostics Windows service: %w", err)
		}
	}
	return nil
}

func install() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	diagnosticError := uninstallDiagnosticService(m)
	if diagnosticError != nil {
		return diagnosticError
	}

	handles := make([]*mgr.Service, len(services))
	for i, s := range services {
		// Registering with the event log is required to suppress the "The description for Event ID 1 from source Google Cloud Ops Agent cannot be found" message in the logs.
		if err := eventlog.InstallAsEventCreate(s.name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
			// Ignore error since it likely means the event log already exists.
		}
		deps := []string{"rpcss"}
		if i > 0 {
			// All services depend on the config generation service.
			deps = append(deps, services[0].name)
		}
		serviceHandle, err := m.OpenService(s.name)
		if err == nil {
			// Service already exists; just update its configuration.
			defer serviceHandle.Close()
			config, err := serviceHandle.Config()
			if err != nil {
				return err
			}
			config.DisplayName = s.displayName
			config.BinaryPathName = escapeExe(s.exepath, s.args)
			config.Dependencies = deps
			config.DelayedAutoStart = true
			if err := serviceHandle.UpdateConfig(config); err != nil {
				return err
			}
		} else {
			serviceHandle, err = m.CreateService(
				s.name,
				s.exepath,
				mgr.Config{
					DisplayName:      s.displayName,
					StartType:        mgr.StartAutomatic,
					Dependencies:     deps,
					DelayedAutoStart: true,
				},
				s.args...,
			)
			if err != nil {
				return err
			}
			defer serviceHandle.Close()
		}
		// restart after 1s then 2s, reset error counter after 60s
		serviceHandle.SetRecoveryActions([]mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: time.Second},
			{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		}, 60)
		handles[i] = serviceHandle
	}

	// Automatically (re)start the Ops Agent service.
	for i := len(services) - 1; i >= 0; i-- {
		if err := stopService(handles[i], 30*time.Second); err != nil {
			return fmt.Errorf("failed to stop service: %v, error: %v", services[i].name, err)
		}
	}
	return handles[0].Start()
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	var errs error

	// Have to remove the services in reverse order.
	for i := len(services) - 1; i >= 0; i-- {
		s := services[i]
		serviceHandle, err := m.OpenService(s.name)
		if err != nil {
			// Service does not exist, so nothing to delete.
			continue
		}
		defer serviceHandle.Close()
		if err := stopService(serviceHandle, 30*time.Second); err != nil {
			// Don't return until all services have been processed.
			errs = multierror.Append(errs, err)
		}
		if err := serviceHandle.Delete(); err != nil {
			// Don't return until all services have been processed.
			errs = multierror.Append(errs, err)
		}
	}

	diagnosticError := uninstallDiagnosticService(m)
	if diagnosticError != nil {
		errs = multierror.Append(errs, diagnosticError)
	}
	return errs
}

func stopService(serviceHandle *mgr.Service, timeout time.Duration) error {
	var status svc.Status
	var err error
	if status, err = serviceHandle.Query(); err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status, err = serviceHandle.Control(svc.Stop); err != nil {
		return err
	}
	startTime := time.Now()
	for status.State != svc.Stopped {
		if time.Since(startTime) > timeout {
			return fmt.Errorf("Timed out (>%v) waiting for service to stop", timeout)
		}
		time.Sleep(1 * time.Second)
		if status, err = serviceHandle.Query(); err != nil {
			return err
		}
	}
	return nil
}
