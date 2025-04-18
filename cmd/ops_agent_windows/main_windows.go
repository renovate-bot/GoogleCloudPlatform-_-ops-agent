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
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/ops-agent/internal/healthchecks"
	"github.com/GoogleCloudPlatform/ops-agent/internal/logs"
	"github.com/kardianos/osext"
	"golang.org/x/sys/windows/svc"
)

const dataDirectory = `Google/Cloud Operations/Ops Agent`
const serviceName = "google-cloud-ops-agent"
const serviceDisplayName = "Google Cloud Ops Agent"

var (
	installServices   = flag.Bool("install", false, "whether to install the services")
	uninstallServices = flag.Bool("uninstall", false, "whether to uninstall the services")
	healthChecks      = flag.Bool("healthchecks", false, "run health checks and exit")
)

func main() {

	infoLog := logs.NewSimpleLogger()

	if ok, err := svc.IsWindowsService(); ok && err == nil {
		if err := run(serviceName); err != nil {
			log.Fatal(err)
		}
	} else if err != nil {
		log.Fatalf("failed to talk to service control manager: %v", err)
	} else {
		flag.Parse()
		if *installServices && *uninstallServices {
			log.Fatal("Can't use both --install and --uninstall")
		}
		if *installServices {
			if err := install(); err != nil {
				log.Fatal(err)
			}
			infoLog.Printf("installed services")
		} else if *uninstallServices {
			if err := uninstall(); err != nil {
				log.Fatal(err)
			}
			infoLog.Printf("uninstalled services")
		} else if *healthChecks {
			healthCheckResults := getHealthCheckResults()
			healthchecks.LogHealthCheckResults(healthCheckResults, infoLog)
			infoLog.Println("Health checks finished")
		} else {
			// TODO: add an interactive GUI box with the Install, Uninstall, and Cancel buttons.
			fmt.Println("Invoked as a standalone program with no flags. Nothing to do.")
			fmt.Println("Use either --healthchecks, --install, --uninstall to take action.")
		}
	}
}

var services []struct {
	name        string
	displayName string
	exepath     string
	args        []string
}

func init() {
	if err := initServices(); err != nil {

		log.Fatal(err)
	}
}

func initServices() error {
	// Identify relevant paths
	self, err := osext.Executable()
	if err != nil {
		return fmt.Errorf("could not determine own path: %w", err)
	}
	base, err := osext.ExecutableFolder()
	if err != nil {
		return fmt.Errorf("could not determine binary path: %w", err)
	}
	configOutDir := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, "generated_configs")
	if err := os.MkdirAll(configOutDir, 0644); err != nil {
		return err
	}
	fluentbitStoragePath := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, `run\buffers`)
	if err := os.MkdirAll(fluentbitStoragePath, 0644); err != nil {
		return err
	}
	logDirectory := filepath.Join(os.Getenv("PROGRAMDATA"), dataDirectory, "log")
	if err := os.MkdirAll(logDirectory, 0644); err != nil {
		return err
	}

	// TODO: Write meaningful descriptions for these services
	services = []struct {
		name        string
		displayName string
		exepath     string
		args        []string
	}{
		{
			serviceName,
			serviceDisplayName,
			self,
			[]string{
				"-in", filepath.Join(base, "../config/config.yaml"),
				"-out", configOutDir,
			},
		},
		{
			fmt.Sprintf("%s-opentelemetry-collector", serviceName),
			fmt.Sprintf("%s - Metrics Agent", serviceDisplayName),
			filepath.Join(base, "google-cloud-metrics-agent_windows_amd64.exe"),
			[]string{
				"--config=" + filepath.Join(configOutDir, `otel\otel.yaml`),
			},
		},
		{
			// TODO: fluent-bit hardcodes a service name of "fluent-bit"; do we need to match that?
			fmt.Sprintf("%s-fluent-bit", serviceName),
			fmt.Sprintf("%s - Logging Agent", serviceDisplayName),
			filepath.Join(base, fmt.Sprintf("%s-wrapper.exe", serviceName)),
			[]string{
				"-log_path", filepath.Join(logDirectory, "logging-module.log"),
				"-config_path", filepath.Join(base, "../config/config.yaml"),
				filepath.Join(base, "fluent-bit.exe"),
				"-c", filepath.Join(configOutDir, `fluentbit\fluent_bit_main.conf`),
				"-R", filepath.Join(configOutDir, `fluentbit\fluent_bit_parser.conf`),
				"--storage_path", fluentbitStoragePath,
			},
		},
	}
	return nil
}
