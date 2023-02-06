package cmd

import (
	"context"
	"fmt"
	"github.com/loft-sh/devpod/cmd/flags"
	"github.com/loft-sh/devpod/pkg/agent"
	"github.com/loft-sh/devpod/pkg/config"
	"github.com/loft-sh/devpod/pkg/log"
	provider2 "github.com/loft-sh/devpod/pkg/provider"
	"github.com/loft-sh/devpod/pkg/template"
	workspace2 "github.com/loft-sh/devpod/pkg/workspace"
	"github.com/loft-sh/devpod/scripts"
	"github.com/spf13/cobra"
	"io"
	"os"
	"os/exec"
)

// UpCmd holds the up cmd flags
type UpCmd struct {
	flags.GlobalFlags

	Snapshot bool
}

// NewUpCmd creates a new up command
func NewUpCmd(flags *flags.GlobalFlags) *cobra.Command {
	cmd := &UpCmd{
		GlobalFlags: *flags,
	}
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Starts a new workspace",
		RunE: func(_ *cobra.Command, args []string) error {
			devPodConfig, err := config.LoadConfig(cmd.Context)
			if err != nil {
				return err
			}

			workspace, provider, err := workspace2.ResolveWorkspace(devPodConfig, args, log.Default)
			if err != nil {
				return err
			}

			return cmd.Run(context.Background(), workspace, provider)
		},
	}

	upCmd.Flags().BoolVar(&cmd.Snapshot, "snapshot", false, "If true will create a snapshot for the environment")
	return upCmd
}

// Run runs the command logic
func (cmd *UpCmd) Run(ctx context.Context, workspace *provider2.Workspace, provider provider2.Provider) error {
	// make sure instance is running before we continue
	err := startWait(ctx, provider, workspace, true, log.Default)
	if err != nil {
		return err
	}

	// run devpod agent up
	err = devPodUp(ctx, provider, workspace, log.Default)
	if err != nil {
		return err
	}

	// configure container ssh
	err = configureSSH(workspace.Context, workspace.ID, "vscode")
	if err != nil {
		return err
	}
	log.Default.Infof("Run 'ssh %s.devpod' to ssh into the devcontainer", workspace.ID)

	// start VSCode
	log.Default.Infof("Starting VSCode...")
	err = exec.Command("code", "--folder-uri", fmt.Sprintf("vscode-remote://ssh-remote+%s.devpod/workspaces/%s", workspace.ID, workspace.ID)).Run()
	if err != nil {
		return err
	}

	return nil
}

func devPodUp(ctx context.Context, provider provider2.Provider, workspace *provider2.Workspace, log log.Logger) error {
	serverProvider, ok := provider.(provider2.ServerProvider)
	if ok {
		return devPodUpServer(ctx, serverProvider, workspace, log)
	}

	return nil
}

func devPodUpServer(ctx context.Context, provider provider2.ServerProvider, workspace *provider2.Workspace, log log.Logger) error {
	log.Infof("Creating devcontainer...")
	command := fmt.Sprintf("sudo %s agent up --id %s ", agent.RemoteDevPodHelperLocation, workspace.ID)
	if workspace.Source.GitRepository != "" {
		command += "--repository " + workspace.Source.GitRepository
	} else if workspace.Source.Image != "" {
		command += "--image " + workspace.Source.Image
	} else if workspace.Source.LocalFolder != "" {
		command += "--local-folder"
	}

	// install devpod into the ssh machine
	t, err := template.FillTemplate(scripts.InstallDevPodTemplate, map[string]string{
		"BaseUrl": agent.DefaultAgentDownloadURL,
		"Command": command,
	})
	if err != nil {
		return err
	}

	// create pipes
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()

	// create client
	go func() {
		err = agent.StartTunnelServer(stdoutReader, stdinWriter, false, workspace.Source.LocalFolder, log)
		if err != nil {
			log.Errorf("Start tunnel server: %v", err)
		}
	}()

	// create container etc.
	err = provider.Command(ctx, workspace, provider2.CommandOptions{
		Command: t,
		Stdin:   stdinReader,
		Stdout:  stdoutWriter,
		Stderr:  os.Stderr,
	})
	if err != nil {
		return err
	}

	return nil
}
