package graph

import (
	"github.com/fabric8-services/fabric8-auth/authorization"
	resource "github.com/fabric8-services/fabric8-auth/authorization/resource/repository"
	"github.com/stretchr/testify/require"
)

// spaceWrapper represents a space resource domain object
type spaceWrapper struct {
	baseWrapper
	resource       *resource.Resource
	parentResource *resource.Resource
}

func newSpaceWrapper(g *TestGraph, params []interface{}) interface{} {
	w := spaceWrapper{baseWrapper: baseWrapper{g}}

	resourceType, err := g.app.ResourceTypeRepository().Lookup(g.ctx, authorization.ResourceTypeSpace)
	require.NoError(g.t, err)

	var resourceID *string
	for i := range params {
		switch t := params[i].(type) {
		case *organizationWrapper:
			w.parentResource = t.Resource()
		case organizationWrapper:
			w.parentResource = t.Resource()
		case *resourceWrapper:
			w.parentResource = t.Resource()
		case resourceWrapper:
			w.parentResource = t.Resource()
		case *string:
			resourceID = t
		case string:
			resourceID = &t
		}
	}

	var parentResourceID *string
	if w.parentResource != nil {
		parentResourceID = &w.parentResource.ResourceID
	}
	w.resource, err = g.app.ResourceService().Register(g.ctx, resourceType.Name, resourceID, parentResourceID, nil)
	require.NoError(g.t, err)

	return &w
}

func loadSpaceWrapper(g *TestGraph, resourceID string) spaceWrapper {
	w := spaceWrapper{baseWrapper: baseWrapper{g}}

	var native resource.Resource
	err := w.graph.db.Table("resource").Preload("ParentResource").Where("resource_id = ?", resourceID).Find(&native).Error
	require.NoError(w.graph.t, err)

	w.resource = &native
	if w.resource.ParentResource != nil {
		w.parentResource = w.resource.ParentResource
	}

	return w
}

// AddAdmin assigns the admin role to a user for the space
func (w *spaceWrapper) AddAdmin(wrapper interface{}) *spaceWrapper {
	addRoleByName(w.baseWrapper, w.resource, authorization.ResourceTypeSpace, identityIDFromWrapper(w.graph.t, wrapper), authorization.SpaceAdminRole)
	return w
}

// RemoveAdmin removes the admin role to a user for the space
func (w *spaceWrapper) RemoveAdmin(wrapper interface{}) *spaceWrapper {
	removeRoleByName(w.baseWrapper, w.resource, authorization.ResourceTypeSpace, identityIDFromWrapper(w.graph.t, wrapper), authorization.SpaceAdminRole)
	return w
}

// AddContributor assigns the admin role to a user for the space
func (w *spaceWrapper) AddContributor(wrapper interface{}) *spaceWrapper {
	addRoleByName(w.baseWrapper, w.resource, authorization.ResourceTypeSpace, identityIDFromWrapper(w.graph.t, wrapper), authorization.SpaceContributorRole)
	return w
}

// AddViewer assigns the admin role to a user for the space
func (w *spaceWrapper) AddViewer(wrapper interface{}) *spaceWrapper {
	addRoleByName(w.baseWrapper, w.resource, authorization.ResourceTypeSpace, identityIDFromWrapper(w.graph.t, wrapper), authorization.SpaceViewerRole)
	return w
}

// AddRole assigns the given role to a user for the space
func (w *spaceWrapper) AddRole(wrapper interface{}, roleWrapper *roleWrapper) *spaceWrapper {
	addRole(w.baseWrapper, w.resource, authorization.ResourceTypeSpace, identityIDFromWrapper(w.graph.t, wrapper), roleWrapper.Role())
	return w
}

func (w *spaceWrapper) Resource() *resource.Resource {
	return w.resource
}

func (w *spaceWrapper) SpaceID() string {
	return w.resource.ResourceID
}
