// Copyright 2017 Google LLC
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

package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/ianlewis/controllerutil/informers"
	"github.com/ianlewis/controllerutil/logging"

	"github.com/ianlewis/memcached-operator/pkg/apis/ianlewis.org/v1alpha1"
	crdfake "github.com/ianlewis/memcached-operator/pkg/client/clientset/versioned/fake"
	crdinformers "github.com/ianlewis/memcached-operator/pkg/client/informers/externalversions/ianlewis/v1alpha1"
)

func getKey(o interface{}, t *testing.T) string {
	key, err := KeyFunc(o)
	if !assert.NoError(t, err, "must be able to generate key for object") {
		assert.FailNow(t, "failing early")
	}
	return key
}

func alwaysSynced() bool {
	return true
}

// expectedAction is an action that is expected to occur and and optional
// function with additional test code to be run for the action.
type expectedAction struct {
	action core.Action
	f      func(*testing.T, core.Action)
}

func (a *expectedAction) Check(t *testing.T, action core.Action) {
	assert.Condition(t, func() bool { return a.action.Matches(action.GetVerb(), action.GetResource().Resource) }, "action must match")
	assert.Equal(t, action.GetSubresource(), a.action.GetSubresource(), "action subresource must match")
	assert.Equal(t, action.GetNamespace(), a.action.GetNamespace(), "action namespace must match")

	if a.f != nil {
		a.f(t, action)
	}
}

type fixture struct {
	t *testing.T

	client    *fake.Clientset
	crdClient *crdfake.Clientset

	controller *Controller
	informers  informers.SharedInformers
}

// expectActions tests whether the actions given match the expected actions.
func expectActions(t *testing.T, actions []core.Action, expected []expectedAction) {
	for i, action := range actions {
		if len(expected) < i+1 {
			assert.Failf(t, "unexpected actions", "%+v", actions[i:])
			break
		}

		expected[i].Check(t, action)
	}

	if len(expected) > len(actions) {
		assert.Failf(t, "additional expected actions", "%+v", expected[len(actions):])
	}
}

func newFixture(t *testing.T, proxies ...*v1alpha1.MemcachedProxy) *fixture {
	objects := make([]runtime.Object, len(proxies))
	for i, p := range proxies {
		objects[i] = p
	}

	client := fake.NewSimpleClientset()
	crdClient := crdfake.NewSimpleClientset(objects...)

	informers := informers.NewSharedInformers()
	proxyInformer := informers.InformerFor(
		&v1alpha1.MemcachedProxy{},
		func() cache.SharedIndexInformer {
			return crdinformers.NewMemcachedProxyInformer(
				crdClient,
				corev1.NamespaceAll,
				0,
				cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			)
		},
	)
	for _, p := range proxies {
		proxyInformer.GetIndexer().Add(p)
	}

	c := New(
		"test-proxy-controller",
		client,
		crdClient,
		proxyInformer,
		&record.FakeRecorder{},
		logging.New(""),
		1,
	)

	c.pListerSynced = alwaysSynced

	return &fixture{
		t:          t,
		client:     client,
		crdClient:  crdClient,
		controller: c,
		informers:  informers,
	}
}

func (f *fixture) runSync(key string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		err := f.informers.Run(ctx)
		assert.NoError(f.t, err, "informers must start successfully")
	}()

	err := f.controller.syncHandler(key)
	assert.NoError(f.t, err, "syncHandler must complete successfully")
}

// TestNewProxy tests the handling of the memcached proxy controller when a new memcached proxy is created
func TestNewProxy(t *testing.T) {
	p := &v1alpha1.MemcachedProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hoge",
			Namespace: metav1.NamespaceDefault,
		},
		Spec: v1alpha1.MemcachedProxySpec{
			Rules: v1alpha1.RuleSpec{
				Service: &v1alpha1.ServiceSpec{
					Name: "fuga",
				},
			},
		},
	}

	f := newFixture(t, p)

	f.runSync(getKey(p, t))

	actions := f.crdClient.Actions()
	expectActions(t, actions, []expectedAction{
		{
			core.NewUpdateAction(schema.GroupVersionResource{Resource: "memcachedproxies"}, p.Namespace, p),
			func(t *testing.T, action core.Action) {
				a := action.(core.UpdateAction)
				pNew := a.GetObject().(*v1alpha1.MemcachedProxy)
				assert.True(t, pNew.Status.Initialized, "memcached proxy must be initialized")

				// Check that the default rule type was set
				assert.Equal(t, pNew.Spec.Rules.Type, v1alpha1.ShardedRuleType, "rules type must be equal")

				// Check that the observed hash was set
				assert.NotEmpty(t, pNew.Status.ObservedSpecHash, "observed spec hash must be set")
			},
		},
	})
}
