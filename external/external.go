package external

import cmtypes "github.com/YLonely/cer-manager/api/types"

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
	Namespaces map[cmtypes.NamespaceType]NamespaceInfo
	Checkpoint *CheckpointInfo
}

const ResourcesExtensionKey = "ExternalResources"
