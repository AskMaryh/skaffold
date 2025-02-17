/*
Copyright 2019 The Skaffold Authors

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

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/kubectl/pkg/util/templates"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/color"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/config"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	sErrors "github.com/GoogleContainerTools/skaffold/pkg/skaffold/errors"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/instrumentation"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/runner/runcontext"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/server"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/survey"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/update"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/version"
)

var (
	opts              config.SkaffoldOptions
	v                 string
	defaultColor      int
	forceColors       bool
	overwrite         bool
	interactive       bool
	timestamps        bool
	shutdownAPIServer func() error
)

// Annotation for commands that should allow post execution housekeeping messages like updates and surveys
const (
	HouseKeepingMessagesAllowedAnnotation = "skaffold_annotation_housekeeping_allowed"
)

func NewSkaffoldCommand(out, errOut io.Writer) *cobra.Command {
	updateMsg := make(chan string, 1)
	surveyPrompt := make(chan bool, 1)
	metricsPrompt := make(chan bool, 1)

	rootCmd := &cobra.Command{
		Use: "skaffold",
		Long: `A tool that facilitates continuous development for Kubernetes applications.

  Find more information at: https://skaffold.dev/docs/getting-started/`,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.Root().SilenceUsage = true

			opts.Command = cmd.Use
			instrumentation.SetCommand(cmd.Use)
			out := color.SetupColors(out, defaultColor, forceColors)
			if timestamps {
				l := logrus.New()
				l.SetOutput(out)
				l.SetFormatter(&logrus.TextFormatter{
					DisableTimestamp: false,
				})
				out = l.Writer()
			}
			cmd.Root().SetOutput(out)

			// Setup logs
			if err := setUpLogs(errOut, v, timestamps); err != nil {
				return err
			}

			// Start API Server
			shutdown, err := server.Initialize(opts)
			if err != nil {
				return fmt.Errorf("initializing api server: %w", err)
			}
			shutdownAPIServer = shutdown

			// Print version
			versionInfo := version.Get()
			logrus.Infof("Skaffold %+v", versionInfo)
			if !isHouseKeepingMessagesAllowed(cmd) {
				logrus.Debugf("Disable housekeeping messages for command explicitly")
				return nil
			}
			switch {
			case preReleaseVersion(versionInfo.Version) && !update.EnableCheck:
				logrus.Debugf("Update check, survey prompt and telemetry disabled for pre release version")
			case !interactive:
				logrus.Debugf("Update check, survey prompt and telemetry disabled in non-interactive mode")
			case quietFlag:
				logrus.Debugf("Update check, survey prompt and telemetry disabled in quiet mode")
			case analyze:
				logrus.Debugf("Update check, survey prompt and telemetry disabled disabled when running `init --analyze`")
			default:
				go func() {
					msg, err := update.CheckVersion(opts.GlobalConfig)
					if err != nil {
						logrus.Infof("update check failed: %s", err)
					} else if msg != "" {
						updateMsg <- msg
					}
					surveyPrompt <- config.ShouldDisplayPrompt(opts.GlobalConfig)
					metricsPrompt <- instrumentation.ShouldDisplayMetricsPrompt(opts.GlobalConfig)
				}()
			}
			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			select {
			case msg := <-updateMsg:
				if err := config.UpdateMsgDisplayed(opts.GlobalConfig); err != nil {
					logrus.Debugf("could not update the 'last-prompted' config for 'update-config' section due to %s", err)
				}
				fmt.Fprintf(cmd.OutOrStderr(), "%s\n", msg)
			default:
			}
			// check if survey prompt needs to be displayed
			select {
			case shouldDisplay := <-surveyPrompt:
				if shouldDisplay {
					if err := survey.New(opts.GlobalConfig).DisplaySurveyPrompt(cmd.OutOrStdout()); err != nil {
						fmt.Fprintf(cmd.OutOrStderr(), "%v\n", err)
					}
				}
			default:
			}
			select {
			case showMetricsPrompt := <-metricsPrompt:
				if showMetricsPrompt {
					if err := instrumentation.DisplayMetricsPrompt(opts.GlobalConfig, cmd.OutOrStdout()); err != nil {
						fmt.Fprintf(cmd.OutOrStderr(), "%v\n", err)
					}
				}
			default:
			}
		},
	}

	groups := templates.CommandGroups{
		{
			Message: "End-to-end pipelines:",
			Commands: []*cobra.Command{
				NewCmdRun(),
				NewCmdDev(),
				NewCmdDebug(),
			},
		},
		{
			Message: "Pipeline building blocks for CI/CD:",
			Commands: []*cobra.Command{
				NewCmdBuild(),
				NewCmdTest(),
				NewCmdDeploy(),
				NewCmdDelete(),
				NewCmdRender(),
				NewCmdApply(),
			},
		},
		{
			Message: "Getting started with a new project:",
			Commands: []*cobra.Command{
				NewCmdInit(),
				NewCmdFix(),
			},
		},
	}
	groups.Add(rootCmd)

	// other commands
	rootCmd.AddCommand(NewCmdVersion())
	rootCmd.AddCommand(NewCmdCompletion())
	rootCmd.AddCommand(NewCmdConfig())
	rootCmd.AddCommand(NewCmdFindConfigs())
	rootCmd.AddCommand(NewCmdDiagnose())
	rootCmd.AddCommand(NewCmdOptions())
	rootCmd.AddCommand(NewCmdCredits())
	rootCmd.AddCommand(NewCmdSchema())
	rootCmd.AddCommand(NewCmdFilter())

	rootCmd.AddCommand(NewCmdGeneratePipeline())
	rootCmd.AddCommand(NewCmdSurvey())
	rootCmd.AddCommand(NewCmdInspect())

	templates.ActsAsRootCommand(rootCmd, nil, groups...)
	rootCmd.PersistentFlags().StringVarP(&v, "verbosity", "v", constants.DefaultLogLevel.String(), "Log level (debug, info, warn, error, fatal, panic)")
	rootCmd.PersistentFlags().IntVar(&defaultColor, "color", int(color.DefaultColorCode), "Specify the default output color in ANSI escape codes")
	rootCmd.PersistentFlags().BoolVar(&forceColors, "force-colors", false, "Always print color codes (hidden)")
	rootCmd.PersistentFlags().BoolVar(&interactive, "interactive", true, "Allow user prompts for more information")
	rootCmd.PersistentFlags().BoolVar(&update.EnableCheck, "update-check", true, "Check for a more recent version of Skaffold")
	rootCmd.PersistentFlags().BoolVar(&timestamps, "timestamps", false, "Print timestamps in logs.")
	rootCmd.PersistentFlags().MarkHidden("force-colors")

	setFlagsFromEnvVariables(rootCmd)

	return rootCmd
}

func NewCmdOptions() *cobra.Command {
	cmd := &cobra.Command{
		Use: "options",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Usage()
		},
	}
	templates.UseOptionsTemplates(cmd)

	return cmd
}

// Each flag can also be set with an env variable whose name starts with `SKAFFOLD_`.
func setFlagsFromEnvVariables(rootCmd *cobra.Command) {
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		envVar := FlagToEnvVarName(f)
		if val, present := os.LookupEnv(envVar); present {
			rootCmd.PersistentFlags().Set(f.Name, val)
		}
	})
	for _, cmd := range rootCmd.Commands() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			// special case for backward compatibility.
			if f.Name == "namespace" {
				if val, present := os.LookupEnv("SKAFFOLD_DEPLOY_NAMESPACE"); present {
					logrus.Warnln("Using SKAFFOLD_DEPLOY_NAMESPACE env variable is deprecated. Please use SKAFFOLD_NAMESPACE instead.")
					cmd.Flags().Set(f.Name, val)
				}
			}

			envVar := FlagToEnvVarName(f)
			if val, present := os.LookupEnv(envVar); present {
				cmd.Flags().Set(f.Name, val)
			}
		})
	}
}

func FlagToEnvVarName(f *pflag.Flag) string {
	return fmt.Sprintf("SKAFFOLD_%s", strings.ReplaceAll(strings.ToUpper(f.Name), "-", "_"))
}

func setUpLogs(stdErr io.Writer, level string, timestamp bool) error {
	logrus.SetOutput(stdErr)
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("parsing log level: %w", err)
	}
	logrus.SetLevel(lvl)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: timestamp,
	})
	return nil
}

// alwaysSucceedWhenCancelled returns nil if the context was cancelled.
// If the error is due to cancellation, return it as it gets swallowed
// in skaffold main.
// For all other errors, pass through known errors.
// TODO: Return nil if error is `context.Cancelled` and remove check in main.
func alwaysSucceedWhenCancelled(ctx context.Context, runCtx *runcontext.RunContext, err error) error {
	if err == nil {
		return err
	}
	// if the context was cancelled act as if all is well
	if ctx.Err() == context.Canceled {
		return nil
	} else if err == context.Canceled {
		return err
	}
	return sErrors.ShowAIError(runCtx, err)
}

func isHouseKeepingMessagesAllowed(cmd *cobra.Command) bool {
	if cmd.Annotations == nil {
		return false
	}
	return cmd.Annotations[HouseKeepingMessagesAllowedAnnotation] == "true"
}

func allowHouseKeepingMessages(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[HouseKeepingMessagesAllowedAnnotation] = "true"
}

func preReleaseVersion(s string) bool {
	if v, err := version.ParseVersion(s); err == nil && len(v.Pre) > 0 {
		return true
	} else if err != nil {
		return true
	}
	return false
}
