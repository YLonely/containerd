package namespaces

import cermns "github.com/YLonely/cer-manager/api/types"

type ExternalNamespaceInfo struct {
	ID   int
	Path string
	Info interface{}
}

type ExternalNamespaces map[cermns.NamespaceType]ExternalNamespaceInfo

const ExternalNamespacesExtensionKey = "ExternalNamespaces"
