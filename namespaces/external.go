package namespaces

import cermns "github.com/YLonely/cer-manager/namespace"

type ExternalNamespaceInfo struct {
	ID   int
	Path string
	Info interface{}
}

type ExternalNamespaces map[cermns.NamespaceType]ExternalNamespaceInfo

const ExternalNamespacesExtensionKey = "ExternalNamespaces"
