/*
Copyright 2021 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package inspect

import (
	"context"
	"io"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/config"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
)

type profileList struct {
	Profiles []profileEntry `json:"profiles"`
}

type profileEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Module string `json:"module,omitempty"`
}

func PrintProfilesList(ctx context.Context, out io.Writer, opts Options) error {
	formatter := getOutputFormatter(out, opts.OutFormat)
	cfgs, err := getConfigSetFunc(config.SkaffoldOptions{ConfigurationFile: opts.Filename})
	if err != nil {
		return formatter.WriteErr(err)
	}

	l := &profileList{Profiles: []profileEntry{}}
	for _, c := range cfgs {
		if len(opts.Modules) > 0 && !util.StrSliceContains(opts.Modules, c.Metadata.Name) {
			continue
		}
		for _, p := range c.Profiles {
			if opts.BuildEnv != BuildEnvs.Unspecified && GetBuildEnv(&p.Build.BuildType) != opts.BuildEnv {
				continue
			}
			l.Profiles = append(l.Profiles, profileEntry{Name: p.Name, Path: c.SourceFile, Module: c.Metadata.Name})
		}
	}
	return formatter.Write(l)
}
