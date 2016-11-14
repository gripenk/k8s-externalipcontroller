// Copyright 2016 Mirantis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scheduler

import (
	"testing"
	"time"

	"runtime"

	"github.com/Mirantis/k8s-externalipcontroller/pkg/extensions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/watch"
	fcache "k8s.io/client-go/1.5/tools/cache/testing"
)

type fakeExtClientset struct {
	mock.Mock
	ipclaims *fakeIpClaims
	ipnodes  *fakeIpNodes
}

func (f *fakeExtClientset) IPClaims() extensions.IPClaimsInterface {
	return f.ipclaims
}

func (f *fakeExtClientset) IPNodes() extensions.IPNodesInterface {
	return f.ipnodes
}

type fakeIpClaims struct {
	mock.Mock
}

func (f *fakeIpClaims) Create(ipclaim *extensions.IpClaim) (*extensions.IpClaim, error) {
	args := f.Called(ipclaim)
	return ipclaim, args.Error(0)
}

func (f *fakeIpClaims) List(opts api.ListOptions) (*extensions.IpClaimList, error) {
	args := f.Called(opts)
	return args.Get(0).(*extensions.IpClaimList), args.Error(1)
}

func (f *fakeIpClaims) Update(ipclaim *extensions.IpClaim) (*extensions.IpClaim, error) {
	args := f.Called(ipclaim)
	return ipclaim, args.Error(0)
}

func (f *fakeIpClaims) Delete(name string, opts *api.DeleteOptions) error {
	args := f.Called(name, opts)
	return args.Error(0)
}

func (f *fakeIpClaims) Watch(_ api.ListOptions) (watch.Interface, error) {
	return nil, nil
}

type fakeIpNodes struct {
	mock.Mock
}

func (f *fakeIpNodes) Create(ipnode *extensions.IpNode) (*extensions.IpNode, error) {
	args := f.Called(ipnode)
	return args.Get(0).(*extensions.IpNode), args.Error(1)
}

func (f *fakeIpNodes) List(opts api.ListOptions) (*extensions.IpNodeList, error) {
	args := f.Called(opts)
	return args.Get(0).(*extensions.IpNodeList), args.Error(1)
}

func (f *fakeIpNodes) Update(ipnode *extensions.IpNode) (*extensions.IpNode, error) {
	args := f.Called(ipnode)
	return args.Get(0).(*extensions.IpNode), args.Error(1)
}

func (f *fakeIpNodes) Delete(name string, opts *api.DeleteOptions) error {
	args := f.Called(name, opts)
	return args.Error(0)
}

func (f *fakeIpNodes) Watch(_ api.ListOptions) (watch.Interface, error) {
	return nil, nil
}

func newFakeExtClientset() *fakeExtClientset {
	return &fakeExtClientset{
		ipclaims: &fakeIpClaims{},
		ipnodes:  &fakeIpNodes{},
	}
}

func TestServiceWatcher(t *testing.T) {
	ext := newFakeExtClientset()
	lw := fcache.NewFakeControllerSource()
	stop := make(chan struct{})
	s := ipClaimScheduler{
		DefaultMask:         "24",
		serviceSource:       lw,
		ExtensionsClientset: ext,
	}
	ext.ipclaims.On("Create", mock.Anything).Return(nil)
	go s.serviceWatcher(stop)
	defer close(stop)

	svc := &v1.Service{
		ObjectMeta: v1.ObjectMeta{Name: "test0"},
		Spec:       v1.ServiceSpec{ExternalIPs: []string{"10.10.0.2"}}}
	lw.Add(svc)
	// let controller to process all services
	runtime.Gosched()
	assert.Equal(t, len(ext.ipclaims.Calls), 1, "Unexpected call count to ipclaims")
	createCall := ext.ipclaims.Calls[0]
	ipclaim := createCall.Arguments[0].(*extensions.IpClaim)
	assert.Equal(t, ipclaim.Spec.Cidr, "10.10.0.2/24", "Unexpected cidr assigned to node")
	assert.Equal(t, ipclaim.Name, "10.10.0.2-24", "Unexpected name")

	ext.ipclaims.On("Delete", "10.10.0.2-24", mock.Anything).Return(nil)
	lw.Delete(svc)
	runtime.Gosched()
	assert.Equal(t, len(ext.ipclaims.Calls), 2, "Unexpected call count to ipclaims")
	deleteCall := ext.ipclaims.Calls[1]
	ipclaimName := deleteCall.Arguments[0].(string)
	assert.Equal(t, ipclaimName, "10.10.0.2-24", "Unexpected name")
}

func TestClaimWatcher(t *testing.T) {
	ext := newFakeExtClientset()
	lw := fcache.NewFakeControllerSource()
	stop := make(chan struct{})
	s := ipClaimScheduler{
		claimSource:         lw,
		ExtensionsClientset: ext,
	}
	go s.claimWatcher(stop)
	defer close(stop)
	claim := &extensions.IpClaim{
		ObjectMeta: v1.ObjectMeta{Name: "10.10.0.2-24"},
		Spec:       extensions.IpClaimSpec{Cidr: "10.10.0.2/24"},
	}
	lw.Add(claim)
	ipnodesList := &extensions.IpNodeList{
		Items: []extensions.IpNode{
			{
				ObjectMeta: v1.ObjectMeta{Name: "first"},
			},
		},
	}
	ext.ipnodes.On("List", mock.Anything).Return(ipnodesList, nil)
	ext.ipclaims.On("Update", mock.Anything).Return(nil)
	runtime.Gosched()
	assert.Equal(t, len(ext.ipclaims.Calls), 1, "Unexpected calls to ipclaims")
	updatedClaim := ext.ipclaims.Calls[0].Arguments[0].(*extensions.IpClaim)
	assert.Equal(t, updatedClaim.Labels, map[string]string{"ipnode": "first"},
		"Labels should be set to scheduled node")
	assert.Equal(t, updatedClaim.Spec.NodeName, "first", "NodeName should be set to scheduled node")
}

func TestMonitorIpNodes(t *testing.T) {
	ext := newFakeExtClientset()
	stop := make(chan struct{})
	ticker := make(chan time.Time, 2)
	for i := 0; i < 2; i++ {
		ticker <- time.Time{}
	}
	s := ipClaimScheduler{
		ExtensionsClientset: ext,
		liveIpNodes:         make(map[string]struct{}),
		observedGeneration:  make(map[string]int64),
	}
	ipnodesList := &extensions.IpNodeList{
		Items: []extensions.IpNode{
			{
				ObjectMeta: v1.ObjectMeta{
					Name:       "first",
					Generation: 666,
				},
			},
		},
	}
	ipclaimsList := &extensions.IpClaimList{
		Items: []extensions.IpClaim{
			{
				ObjectMeta: v1.ObjectMeta{
					Name:   "10.10.0.1-24",
					Labels: map[string]string{"ipnode": "first"},
				},
				Spec: extensions.IpClaimSpec{
					Cidr:     "10.10.0.1/24",
					NodeName: "first",
				},
			},
			{
				ObjectMeta: v1.ObjectMeta{
					Name:   "10.10.0.2-24",
					Labels: map[string]string{"ipnode": "first"},
				},
				Spec: extensions.IpClaimSpec{
					Cidr:     "10.10.0.2/24",
					NodeName: "first",
				},
			},
		},
	}
	ext.ipnodes.On("List", mock.Anything).Return(ipnodesList, nil).Twice()
	ext.ipclaims.On("List", mock.Anything).Return(ipclaimsList, nil)
	ext.ipclaims.On("Update", mock.Anything).Return(nil).Twice()
	go s.monitorIPNodes(stop, ticker)
	defer close(stop)
	runtime.Gosched()
	assert.Equal(t, 2, len(ext.ipnodes.Calls), "Unexpected calls to ipnodes")
	assert.Equal(t, 3, len(ext.ipclaims.Calls), "Unexpected calls to ipclaims")
	updateCalls := ext.ipclaims.Calls[1:]
	for _, call := range updateCalls {
		ipclaim := call.Arguments[0].(*extensions.IpClaim)
		assert.Equal(t, ipclaim.Labels, map[string]string{}, "monitor should clean all ipclaim labels")
		assert.Equal(t, ipclaim.Spec.NodeName, "", "monitor should clean node name")
	}
	assert.Equal(t, s.isLive("first"), false, "first node shouldn't be considered live")
}
