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

package metadata

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/go-playground/validator/v10"
	yaml "github.com/goccy/go-yaml"
	"go.uber.org/multierr"
)

// MetricLabel encodes a specification of a metric label in the metrics backend.
type MetricLabel struct {
	// The label name, for example state.
	Name string `yaml:"name" validate:"required"`
	// The label value pattern, as an RE2 regular expression.
	ValueRegex string `yaml:"value_regex" validate:"required"`
	// The description of the label.
	Description string `yaml:"description" validate:"excludesall=‘’“”"`
	// Annotations/footnotes about the label.
	Notes []string `yaml:"notes,omitempty" validate:"omitempty,unique"`
}

// MetricSpec encodes a specification of a metric in the metrics backend.
type MetricSpec struct {
	// The metric type, for example workload.googleapis.com/apache.current_connections.
	Type string `yaml:"type" validate:"required"`
	// The value type, for example INT64.
	ValueType string `yaml:"value_type" validate:"required,oneof=BOOL INT64 DOUBLE STRING DISTRIBUTION"`
	// The kind, for example GAUGE.
	Kind string `yaml:"kind" validate:"required,oneof=GAUGE DELTA CUMULATIVE"`
	// The unit of the metric.
	Unit string `yaml:"unit"`
	// The description of the metric.
	Description string `yaml:"description" validate:"excludesall=‘’“”"`
	// The monitored resource, for example gce_instance.
	// Currently we only test with gce_instance.
	MonitoredResources []string `yaml:"monitored_resources,flow" validate:"required,gt=0,dive,oneof=gce_instance"`
	// Mapping of expected label keys to label specs.
	Labels []*MetricLabel `yaml:"labels,omitempty" validate:"omitempty,gt=0,unique=Name,dive"`
	// Annotations/footnotes about the metric.
	Notes []string `yaml:"notes,omitempty" validate:"omitempty,unique"`
}

// ExpectedMetric encodes a series of assertions about what data we expect
// to see in the metrics backend.
type ExpectedMetric struct {
	// The metric being described.
	MetricSpec `yaml:",inline"`

	// If Optional is true, the test for this metric will be skipped.
	Optional bool `yaml:"optional,omitempty" validate:"excluded_with=Representative"`
	// Exactly one metric in each expected_metrics.yaml must
	// have Representative set to true. This metric can be used
	// to test that the integration is enabled.
	Representative bool `yaml:"representative,omitempty" validate:"excluded_with=Optional,excluded_with=Platform"`
	// Exclusive metric to a particular kind of platform.
	Platform string `yaml:"platform,omitempty" validate:"excluded_with=Representative,omitempty,oneof=linux windows"`
	// A list of platforms that this metric is not available on.
	// Examples: debian-11. Not valid are linux,windows.
	UnavailableOn []string `yaml:"unavailable_on,omitempty" validate:"excluded_with=Representative"`
}

type LogField struct {
	Name        string `yaml:"name" validate:"required"`
	ValueRegex  string `yaml:"value_regex"`
	Type        string `yaml:"type" validate:"required"`
	Description string `yaml:"description" validate:"excludesall=‘’“”"`
	Optional    bool   `yaml:"optional,omitempty"`
	// A list of platforms that this field is not available on.
	// Examples: debian-11.
	UnavailableOn []string `yaml:"unavailable_on,omitempty"`
	// Annotations/footnotes about the field.
	Notes []string `yaml:"notes,omitempty" validate:"omitempty,unique"`
}

type ExpectedLog struct {
	LogName string      `yaml:"log_name" validate:"required"`
	Fields  []*LogField `yaml:"fields" validate:"required,unique=Name,dive"`
	// Annotations/footnotes about the log.
	Notes []string `yaml:"notes,omitempty" validate:"omitempty,unique"`
}

type MinimumSupportedAgentVersion struct {
	Logging string `yaml:"logging" validate:"required_without=Metrics"`
	Metrics string `yaml:"metrics" validate:"required_without=Logging"`
}

type ConfigurationFields struct {
	Name        string `yaml:"name" validate:"required"`
	Default     string `yaml:"default"`
	Description string `yaml:"description" validate:"required,excludesall=‘’“”"`
}

type InputConfiguration struct {
	Type   string                 `yaml:"type" validate:"required"`
	Fields []*ConfigurationFields `yaml:"fields" validate:"required,dive"`
}

type ConfigurationOptions struct {
	LogsConfiguration    []*InputConfiguration `yaml:"logs" validate:"required_without=MetricsConfiguration,dive"`
	MetricsConfiguration []*InputConfiguration `yaml:"metrics" validate:"required_without=LogsConfiguration,dive"`
}

type ExpectedMetricsContainer struct {
	ExpectedMetrics []*ExpectedMetric `yaml:"expected_metrics" validate:"onetrue=Representative,unique=Type,dive"`
}

type GpuPlatform struct {
	Model     string   `yaml:"model" validate:"required"`
	Platforms []string `yaml:"platforms" validate:"required"`
}

type IntegrationMetadata struct {
	PublicUrl                    string                        `yaml:"public_url"`
	AppUrl                       string                        `yaml:"app_url" validate:"required,url"`
	ShortName                    string                        `yaml:"short_name" validate:"required,excludesall=‘’“”"`
	LongName                     string                        `yaml:"long_name" validate:"required,excludesall=‘’“”"`
	LogoPath                     string                        `yaml:"logo_path"`
	Description                  string                        `yaml:"description" validate:"required,excludesall=‘’“”"`
	ConfigurationOptions         *ConfigurationOptions         `yaml:"configuration_options" validate:"required"`
	ConfigureIntegration         string                        `yaml:"configure_integration"`
	ExpectedLogs                 []*ExpectedLog                `yaml:"expected_logs" validate:"unique=LogName,dive"`
	MinimumSupportedAgentVersion *MinimumSupportedAgentVersion `yaml:"minimum_supported_agent_version"`
	SupportedAppVersion          []string                      `yaml:"supported_app_version" validate:"required,unique,min=1"`
	SupportedOperatingSystems    string                        `yaml:"supported_operating_systems" validate:"required,oneof=linux windows linux_and_windows"`
	PlatformsToSkip              []string                      `yaml:"platforms_to_skip"`
	GpuPlatforms                 []GpuPlatform                 `yaml:"gpu_platforms" validate:"dive"`
	RestartAfterInstall          bool                          `yaml:"restart_after_install"`
	Troubleshoot                 string                        `yaml:"troubleshoot" validate:"excludesall=‘’“”"`

	ExpectedMetricsContainer `yaml:",inline"`
}

func UnmarshalAndValidate(fullPath string, data []byte, i interface{}) error {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

	v := NewIntegrationMetadataValidator()
	// Note: Unmarshaler does not protect when only the key being declared.
	// https://github.com/goccy/go-yaml/issues/313
	if err := yaml.UnmarshalWithOptions(data, i, yaml.DisallowUnknownField(), yaml.Validator(v)); err != nil {
		return fmt.Errorf("%s%w", fullPath, err)
	}
	return nil
}

func SliceContains(slice []string, toFind string) bool {
	for _, entry := range slice {
		if entry == toFind {
			return true
		}
	}
	return false
}

func NewIntegrationMetadataValidator() *validator.Validate {
	v := validator.New()
	_ = v.RegisterValidation("onetrue", func(fl validator.FieldLevel) bool {
		field := fl.Field()
		param := fl.Param()

		if param == "" {
			panic("onetrue must contain an argument")
		}

		switch field.Kind() {

		case reflect.Slice, reflect.Array:
			elem := field.Type().Elem()

			// Ignore the case where this field is not actually specified or is left empty.
			if field.Len() == 0 {
				return true
			}

			if elem.Kind() == reflect.Ptr {
				elem = elem.Elem()
			}

			sf, ok := elem.FieldByName(param)
			if !ok {
				panic(fmt.Sprintf("Invalid field name %s", param))
			}
			if sfTyp := sf.Type; sfTyp.Kind() != reflect.Bool {
				panic(fmt.Sprintf("Field %s is %s, not bool", param, sfTyp))
			}

			count := 0
			for i := 0; i < field.Len(); i++ {
				if reflect.Indirect(field.Index(i)).FieldByName(param).Bool() {
					count++
				}
			}

			return count == 1

		default:
			panic(fmt.Sprintf("Invalid field type %T", field.Interface()))
		}
	})
	return v
}

func AssertMetric(metric *ExpectedMetric, series *monitoringpb.TimeSeries) error {
	var err error
	if series.ValueType.String() != metric.ValueType {
		err = multierr.Append(err, fmt.Errorf("valueType: expected %s but got %s", metric.ValueType, series.ValueType.String()))
	}
	if series.MetricKind.String() != metric.Kind {
		err = multierr.Append(err, fmt.Errorf("kind: expected %s but got %s", metric.Kind, series.MetricKind.String()))
	}
	if !SliceContains(metric.MonitoredResources, series.Resource.Type) {
		err = multierr.Append(err, fmt.Errorf("unexpected monitored_resource: expected %v but got %s", metric.MonitoredResources, series.Resource.Type))
	}
	err = multierr.Append(err, assertMetricLabels(metric, series))
	if err != nil {
		return fmt.Errorf("%s: %w", metric.Type, err)
	}
	return nil
}

func assertMetricLabels(metric *ExpectedMetric, series *monitoringpb.TimeSeries) error {
	var err error
	// Only expected labels must be present
	expectedLabels := make(map[string]bool)
	for _, expectedLabel := range metric.Labels {
		expectedLabels[expectedLabel.Name] = true
	}
	for actualLabel, actualValue := range series.Metric.Labels {
		if !expectedLabels[actualLabel] {
			err = multierr.Append(err, fmt.Errorf("got unexpected label %q with value %q", actualLabel, actualValue))
		}
	}
	// All expected labels must be present and match the given pattern
	for _, expectedLabel := range metric.Labels {
		actualValue, ok := series.Metric.Labels[expectedLabel.Name]
		if !ok {
			err = multierr.Append(err, fmt.Errorf("expected label not found: %s", expectedLabel))
			continue
		}
		match, matchErr := regexp.MatchString(fmt.Sprintf("^(?:%s)$", expectedLabel.ValueRegex), actualValue)
		if matchErr != nil {
			err = multierr.Append(err, fmt.Errorf("error parsing pattern. label=%s, pattern=%s, err=%v",
				expectedLabel.Name,
				expectedLabel.ValueRegex,
				matchErr,
			))
		} else if !match {
			err = multierr.Append(err, fmt.Errorf("error: label value does not match pattern. label=%s, pattern=%s, value=%s",
				expectedLabel.Name,
				expectedLabel.ValueRegex,
				actualValue,
			))
		}
	}
	return err
}
