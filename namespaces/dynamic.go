package namespaces

import (
	crns "github.com/YLonely/cr-daemon/namespace"
)

type DynamicNamespaceInfo struct {
	ID   int
	Info interface{}
}

type DynamicNamespaces map[crns.NamespaceType]DynamicNamespaceInfo
