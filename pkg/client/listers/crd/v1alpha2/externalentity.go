// Copyright 2021 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha2

import (
	v1alpha2 "github.com/vmware-tanzu/antrea/pkg/apis/crd/v1alpha2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// ExternalEntityLister helps list ExternalEntities.
// All objects returned here must be treated as read-only.
type ExternalEntityLister interface {
	// List lists all ExternalEntities in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha2.ExternalEntity, err error)
	// ExternalEntities returns an object that can list and get ExternalEntities.
	ExternalEntities(namespace string) ExternalEntityNamespaceLister
	ExternalEntityListerExpansion
}

// externalEntityLister implements the ExternalEntityLister interface.
type externalEntityLister struct {
	indexer cache.Indexer
}

// NewExternalEntityLister returns a new ExternalEntityLister.
func NewExternalEntityLister(indexer cache.Indexer) ExternalEntityLister {
	return &externalEntityLister{indexer: indexer}
}

// List lists all ExternalEntities in the indexer.
func (s *externalEntityLister) List(selector labels.Selector) (ret []*v1alpha2.ExternalEntity, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha2.ExternalEntity))
	})
	return ret, err
}

// ExternalEntities returns an object that can list and get ExternalEntities.
func (s *externalEntityLister) ExternalEntities(namespace string) ExternalEntityNamespaceLister {
	return externalEntityNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// ExternalEntityNamespaceLister helps list and get ExternalEntities.
// All objects returned here must be treated as read-only.
type ExternalEntityNamespaceLister interface {
	// List lists all ExternalEntities in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha2.ExternalEntity, err error)
	// Get retrieves the ExternalEntity from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha2.ExternalEntity, error)
	ExternalEntityNamespaceListerExpansion
}

// externalEntityNamespaceLister implements the ExternalEntityNamespaceLister
// interface.
type externalEntityNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all ExternalEntities in the indexer for a given namespace.
func (s externalEntityNamespaceLister) List(selector labels.Selector) (ret []*v1alpha2.ExternalEntity, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha2.ExternalEntity))
	})
	return ret, err
}

// Get retrieves the ExternalEntity from the indexer for a given namespace and name.
func (s externalEntityNamespaceLister) Get(name string) (*v1alpha2.ExternalEntity, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha2.Resource("externalentity"), name)
	}
	return obj.(*v1alpha2.ExternalEntity), nil
}
