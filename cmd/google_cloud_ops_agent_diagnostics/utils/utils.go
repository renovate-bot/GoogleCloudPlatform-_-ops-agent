// Copyright 2020 Google LLC
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

package utils

import (
	"context"

	"github.com/GoogleCloudPlatform/ops-agent/apps"
	"github.com/GoogleCloudPlatform/ops-agent/confgenerator"
)

// GetUserAndMergedConfigs if successful will return both the users original
// config and merged config respectively
func GetUserAndMergedConfigs(ctx context.Context, userConfPath string) (*confgenerator.UnifiedConfig, *confgenerator.UnifiedConfig, error) {
	userUc, err := confgenerator.ReadUnifiedConfigFromFile(ctx, userConfPath)
	if err != nil {
		return nil, nil, err
	}
	if userUc == nil {
		userUc = &confgenerator.UnifiedConfig{}
	}

	mergedUc, err := confgenerator.MergeConfFiles(ctx, userConfPath, apps.BuiltInConfStructs)
	if err != nil {
		return nil, nil, err
	}

	return userUc, mergedUc, nil
}
