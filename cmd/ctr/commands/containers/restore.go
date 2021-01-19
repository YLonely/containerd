/*
   Copyright The containerd Authors.

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

package containers

import (
	cmclient "github.com/YLonely/cer-manager/client"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/external"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var restoreCommand = cli.Command{
	Name:      "restore",
	Usage:     "restore a container from checkpoint",
	ArgsUsage: "CONTAINER REF",
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "rw",
			Usage: "restore the rw layer from the checkpoint",
		},
		cli.BoolFlag{
			Name:  "live",
			Usage: "restore the runtime and memory data from the checkpoint",
		},
		cli.StringSliceFlag{
			Name:  "external-ns",
			Usage: "specifiy the type of the namespace {ipc|uts|mnt} that is dynamically chosen while restoring",
		},
		cli.BoolFlag{
			Name:  "external-checkpoint",
			Usage: "get the path of checkpoint files from the container external resources manager",
		},
	},
	Action: func(context *cli.Context) error {
		id := context.Args().First()
		if id == "" {
			return errors.New("container id must be provided")
		}
		ref := context.Args().Get(1)
		if ref == "" {
			return errors.New("ref must be provided")
		}
		client, ctx, cancel, err := commands.NewClient(context)
		if err != nil {
			return err
		}
		defer cancel()

		checkpoint, err := client.GetImage(ctx, ref)
		if err != nil {
			if !errdefs.IsNotFound(err) {
				return err
			}
			// TODO (ehazlett): consider other options (always/never fetch)
			ck, err := client.Fetch(ctx, ref)
			if err != nil {
				return err
			}
			checkpoint = containerd.NewImage(client, ck)
		}

		opts := []containerd.RestoreOpts{
			containerd.WithRestoreRuntime,
		}
		copts := []containerd.NewContainerOpts{}
		specOpts := []oci.SpecOpts{}
		externalMountNamespace := false
		externalResources := external.ResourcesInfo{
			Namespaces: map[specs.LinuxNamespaceType]external.NamespaceInfo{},
		}
		if len(context.StringSlice("external-ns")) == 0 {
			opts = append(opts, containerd.WithRestoreSpec)
		} else {
			cli, err := cmclient.Default()
			if err != nil {
				return err
			}
			defer cli.Close()
			spec, err := containerd.ParseSpecFromCheckpoint(ctx, client.ContentStore(), checkpoint)
			if err != nil {
				return err
			}
			for _, ns := range context.StringSlice("external-ns") {
				switch ns {
				case "mnt":
					externalMountNamespace = true
					if err = handleExternalNamespace(cli, specs.MountNamespace, client, checkpoint, &specOpts, &externalResources); err != nil {
						return err
					}
				case "ipc":
					if err = handleExternalNamespace(cli, specs.IPCNamespace, client, checkpoint, &specOpts, &externalResources); err != nil {
						return err
					}
				case "uts":
					if err = handleExternalNamespace(cli, specs.UTSNamespace, client, checkpoint, &specOpts, &externalResources); err != nil {
						return err
					}
				default:
					return errors.New("invalid namespace type")
				}
			}
			copts = append(copts, containerd.WithSpec(spec, specOpts...))
		}
		copts = append(copts, containerd.WithContainerExtension(external.ResourcesExtensionKey, &externalResources))

		if !externalMountNamespace {
			opts = append(opts, containerd.WithRestoreImage)
		}
		if context.Bool("rw") {
			opts = append(opts, containerd.WithRestoreRW)
		}

		ctr, err := client.Restore(ctx, id, checkpoint, opts, copts...)
		if err != nil {
			return err
		}

		topts := []containerd.NewTaskOpts{}
		if context.Bool("live") {
			topts = append(topts, containerd.WithTaskCheckpoint(checkpoint, context.Bool("external-checkpoint")))
		}

		task, err := ctr.NewTask(ctx, cio.NewCreator(cio.WithStdio), topts...)
		if err != nil {
			return err
		}

		return task.Start(ctx)
	},
}

func handleExternalNamespace(cli *cmclient.Client, t specs.LinuxNamespaceType, client *containerd.Client, checkpoint containerd.Image, specOpts *[]oci.SpecOpts, externalResources *external.ResourcesInfo) error {
	id, path, info, err := external.GetNamespace(cli, t, client.Namespace(), checkpoint.Name())
	if err != nil {
		return errors.Wrap(err, "failed to get external mount namespace")
	}
	*specOpts = append(*specOpts, oci.WithLinuxNamespace(
		specs.LinuxNamespace{
			Type: t,
			Path: path,
		},
	))
	externalResources.Namespaces[t] = external.NamespaceInfo{
		ID:   id,
		Info: info,
		Path: path,
	}
	return nil
}
