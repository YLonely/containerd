package external

import (
	"github.com/YLonely/cer-manager/api/types"
	"github.com/YLonely/cer-manager/client"
	"github.com/containerd/typeurl"
	ptypes "github.com/gogo/protobuf/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

type NamespaceInfo struct {
	ID   int
	Path string
	Info interface{}
}

type CheckpointInfo struct {
	Name      string
	Namespace string
}

type ResourcesInfo struct {
	Namespaces map[specs.LinuxNamespaceType]NamespaceInfo
	Checkpoint *CheckpointInfo
}

const ResourcesExtensionKey = "ExternalResources"

func GetNamespace(cli *client.Client, t specs.LinuxNamespaceType, namespace string, names ...string) (id int, path string, info interface{}, err error) {
	refs := make([]types.Reference, 0, len(names))
	for _, name := range names {
		refs = append(refs, types.NewContainerdReference(
			name,
			namespace,
		))
	}
	var tt types.NamespaceType
	tt, err = NamespaceType(t)
	if err != nil {
		return
	}
	id, path, info, err = cli.GetNamespace(tt, refs[0], refs[1:]...)
	return
}

func GetCheckpoint(cli *client.Client, namespace, name string) (string, error) {
	return cli.GetCheckpoint(types.NewContainerdReference(
		name,
		namespace,
	))
}

func PutNamespace(cli *client.Client, t specs.LinuxNamespaceType, id int) error {
	tt, err := NamespaceType(t)
	if err != nil {
		return err
	}
	return cli.PutNamespace(tt, id)
}

func PutCheckpoint(cli *client.Client, namespace, name string) error {
	return cli.PutCheckpoint(types.NewContainerdReference(
		name,
		namespace,
	))
}

func NamespaceType(t specs.LinuxNamespaceType) (types.NamespaceType, error) {
	var tt types.NamespaceType
	switch t {
	case specs.IPCNamespace:
		tt = types.NamespaceIPC
	case specs.MountNamespace:
		tt = types.NamespaceMNT
	case specs.UTSNamespace:
		tt = types.NamespaceUTS
	default:
		return tt, errors.New("unsupported namespace type")
	}
	return tt, nil
}

func Parse(any *ptypes.Any) (*ResourcesInfo, error) {
	if any == nil {
		return nil, nil
	}
	var i interface{}
	var err error
	i, err = typeurl.UnmarshalAny(any)
	if err != nil {
		return nil, err
	}
	ret, ok := i.(*ResourcesInfo)
	if !ok {
		return nil, errors.New("can't convert any to ExternalResources")
	}
	return ret, nil
}
