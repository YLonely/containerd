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

package containerd

import (
	"context"
	"encoding/json"

	cermns "github.com/YLonely/cer-manager/api/types"
	cermclient "github.com/YLonely/cer-manager/client"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/typeurl"
	"github.com/gogo/protobuf/proto"
	ptypes "github.com/gogo/protobuf/types"
	"github.com/opencontainers/image-spec/identity"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

var (
	// ErrImageNameNotFoundInIndex is returned when the image name is not found in the index
	ErrImageNameNotFoundInIndex = errors.New("image name not found in index")
	// ErrRuntimeNameNotFoundInIndex is returned when the runtime is not found in the index
	ErrRuntimeNameNotFoundInIndex = errors.New("runtime not found in index")
	// ErrSnapshotterNameNotFoundInIndex is returned when the snapshotter is not found in the index
	ErrSnapshotterNameNotFoundInIndex = errors.New("snapshotter not found in index")
)

// RestoreOpts are options to manage the restore operation
type RestoreOpts func(context.Context, string, *Client, Image, *imagespec.Index) NewContainerOpts

// WithRestoreImage restores the image for the container
func WithRestoreImage(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		name, ok := index.Annotations[checkpointImageNameLabel]
		if !ok || name == "" {
			return ErrRuntimeNameNotFoundInIndex
		}
		snapshotter, ok := index.Annotations[checkpointSnapshotterNameLabel]
		if !ok || name == "" {
			return ErrSnapshotterNameNotFoundInIndex
		}
		i, err := client.GetImage(ctx, name)
		if err != nil {
			return err
		}

		diffIDs, err := i.(*image).i.RootFS(ctx, client.ContentStore(), client.platform)
		if err != nil {
			return err
		}
		parent := identity.ChainID(diffIDs).String()
		if _, err := client.SnapshotService(snapshotter).Prepare(ctx, id, parent); err != nil {
			return err
		}
		c.Image = i.Name()
		c.SnapshotKey = id
		c.Snapshotter = snapshotter
		return nil
	}
}

// WithRestoreRuntime restores the runtime for the container
func WithRestoreRuntime(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		name, ok := index.Annotations[checkpointRuntimeNameLabel]
		if !ok {
			return ErrRuntimeNameNotFoundInIndex
		}

		// restore options if present
		m, err := GetIndexByMediaType(index, images.MediaTypeContainerd1CheckpointRuntimeOptions)
		if err != nil {
			if err != ErrMediaTypeNotFound {
				return err
			}
		}
		var options ptypes.Any
		if m != nil {
			store := client.ContentStore()
			data, err := content.ReadBlob(ctx, store, *m)
			if err != nil {
				return errors.Wrap(err, "unable to read checkpoint runtime")
			}
			if err := proto.Unmarshal(data, &options); err != nil {
				return err
			}
		}

		c.Runtime = containers.RuntimeInfo{
			Name:    name,
			Options: &options,
		}
		return nil
	}
}

// WithRestoreSpec restores the spec from the checkpoint for the container
func WithRestoreSpec(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		m, err := GetIndexByMediaType(index, images.MediaTypeContainerd1CheckpointConfig)
		if err != nil {
			return err
		}
		store := client.ContentStore()
		data, err := content.ReadBlob(ctx, store, *m)
		if err != nil {
			return errors.Wrap(err, "unable to read checkpoint config")
		}
		var any ptypes.Any
		if err := proto.Unmarshal(data, &any); err != nil {
			return err
		}
		c.Spec = &any
		return nil
	}
}

// WithRestoreRW restores the rw layer from the checkpoint for the container
func WithRestoreRW(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		// apply rw layer
		rw, err := GetIndexByMediaType(index, imagespec.MediaTypeImageLayerGzip)
		if err != nil {
			return err
		}
		mounts, err := client.SnapshotService(c.Snapshotter).Mounts(ctx, c.SnapshotKey)
		if err != nil {
			return err
		}

		if _, err := client.DiffService().Apply(ctx, *rw, mounts); err != nil {
			return err
		}
		return nil
	}
}

func getExternalNamespace(t cermns.NamespaceType, arg interface{}) (id int, path string, info interface{}, err error) {
	var client *cermclient.Client
	client, err = cermclient.NewDefaultClient()
	if err != nil {
		return
	}
	defer client.Close()
	id, path, info, err = client.GetNamespace(t, arg)
	return
}

func setExternalNamespace(ctx context.Context, client *Client, c *containers.Container, t specs.LinuxNamespaceType, path string) error {
	var spec oci.Spec
	var err error
	if err = json.Unmarshal(c.Spec.Value, &spec); err != nil {
		return err
	}
	ns := specs.LinuxNamespace{
		Type: t,
		Path: path,
	}
	if err := oci.WithLinuxNamespace(ns)(ctx, client, c, &spec); err != nil {
		return err
	}
	c.Spec.Value, err = json.Marshal(spec)
	if err != nil {
		return err
	}
	return nil
}

func setNamespaceExtension(ctx context.Context, client *Client, c *containers.Container, t cermns.NamespaceType, id int, path string, info interface{}) error {
	namespaceInfo := namespaces.ExternalNamespaceInfo{
		ID:   id,
		Path: path,
		Info: info,
	}
	var externalNamespaces *namespaces.ExternalNamespaces
	ok := false
	if c.Extensions != nil {
		if any, exists := c.Extensions[namespaces.ExternalNamespacesExtensionKey]; exists {
			data, err := typeurl.UnmarshalAny(&any)
			if err != nil {
				return err
			}
			externalNamespaces = data.(*namespaces.ExternalNamespaces)
			ok = true
		}
	}
	if !ok {
		externalNamespaces = &namespaces.ExternalNamespaces{}
	}
	(*externalNamespaces)[t] = namespaceInfo
	if err := WithContainerExtension(namespaces.ExternalNamespacesExtensionKey, externalNamespaces)(ctx, client, c); err != nil {
		return err
	}
	return nil
}

func WithExternalUTS(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		if c.Spec == nil {
			return errors.New("empty container spec")
		}
		id, path, _, err := getExternalNamespace(cermns.NamespaceUTS, checkpoint.Name())
		if err != nil {
			return err
		}
		if err := setExternalNamespace(ctx, client, c, specs.UTSNamespace, path); err != nil {
			return err
		}
		if err := setNamespaceExtension(ctx, client, c, cermns.NamespaceUTS, id, path, nil); err != nil {
			return err
		}
		return nil
	}
}

func WithExternalIPC(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		if c.Spec == nil {
			return errors.New("empty container spec")
		}
		id, path, _, err := getExternalNamespace(cermns.NamespaceIPC, checkpoint.Name())
		if err != nil {
			return err
		}
		if err := setExternalNamespace(ctx, client, c, specs.IPCNamespace, path); err != nil {
			return err
		}
		if err := setNamespaceExtension(ctx, client, c, cermns.NamespaceIPC, id, path, nil); err != nil {
			return err
		}
		return nil
	}
}

func WithExternalMNT(ctx context.Context, id string, client *Client, checkpoint Image, index *imagespec.Index) NewContainerOpts {
	return func(ctx context.Context, client *Client, c *containers.Container) error {
		if c.Spec == nil {
			return errors.New("empty container spec")
		}
		id, path, info, err := getExternalNamespace(cermns.NamespaceMNT, checkpoint.Name())
		if err != nil {
			return err
		}
		if err := setExternalNamespace(ctx, client, c, specs.MountNamespace, path); err != nil {
			return err
		}
		if err := setNamespaceExtension(ctx, client, c, cermns.NamespaceMNT, id, path, info); err != nil {
			return err
		}
		c.Image = checkpoint.Name()
		return nil
	}
}
